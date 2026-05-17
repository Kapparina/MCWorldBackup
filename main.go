package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/gorcon/rcon"
	"github.com/joho/godotenv"
)

type config struct {
	RCONAddress  string
	RCONPassword string
	WorldDir     string
	BackupDir    string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))

	if runErr := run(logger); runErr != nil {
		logger.Error("backup failed", slog.Any("error", runErr))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	if loadErr := godotenv.Load(); loadErr != nil && !os.IsNotExist(loadErr) {
		return fmt.Errorf("load .env: %w", loadErr)
	}

	cfg, configErr := loadConfig()
	if configErr != nil {
		return fmt.Errorf("load config: %w", configErr)
	}
	log.Info("starting minecraft backup")
	conn, dialErr := rcon.Dial(cfg.RCONAddress, cfg.RCONPassword)
	if dialErr != nil {
		return fmt.Errorf("connect to rcon: %w", dialErr)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.Warn("failed to close rcon connection", slog.Any("error", closeErr))
		}
	}()
	if saveOffErr := rconCommand(conn, "save-off", log); saveOffErr != nil {
		return fmt.Errorf("disable world saving: %w", saveOffErr)
	}
	saveEnabled := false
	defer func() {
		if saveEnabled {
			return
		}
		if saveOnErr := rconCommand(conn, "save-on", log); saveOnErr != nil {
			log.Error("failed to restore world saving", slog.Any("error", saveOnErr))
			return
		}
		log.Info("world saving restored")
	}()
	if saveAllErr := rconCommand(conn, "save-all", log); saveAllErr != nil {
		return fmt.Errorf("flush world save: %w", saveAllErr)
	}
	time.Sleep(10 * time.Second)
	if mkdirErr := os.MkdirAll(cfg.BackupDir, 0755); mkdirErr != nil {
		return fmt.Errorf("create backup directory: %w", mkdirErr)
	}
	outputPath := filepath.Join(cfg.BackupDir, fmt.Sprintf("world-%s.tar.gz", time.Now().Format("2006-01-02_15-04-05")))
	log.Info("creating archive", slog.String("source", cfg.WorldDir), slog.String("destination", outputPath))
	if archiveErr := tarGz(cfg.WorldDir, outputPath); archiveErr != nil {
		return fmt.Errorf("create archive: %w", archiveErr)
	}
	if validateErr := validateBackup(outputPath, log); validateErr != nil {
		return fmt.Errorf("validate backup: %w", validateErr)
	}
	if saveOnErr := rconCommand(conn, "save-on", log); saveOnErr != nil {
		return fmt.Errorf("enable world saving: %w", saveOnErr)
	}
	saveEnabled = true
	log.Info("backup completed", slog.String("path", outputPath))
	return nil
}

func loadConfig() (config, error) {
	cfg := config{
		RCONAddress:  os.Getenv("MCBACKUP_RCON_ADDRESS"),
		RCONPassword: os.Getenv("MCBACKUP_RCON_PASSWORD"),
		WorldDir:     os.Getenv("MCBACKUP_WORLD_DIR"),
		BackupDir:    os.Getenv("MCBACKUP_BACKUP_DIR"),
	}
	if cfg.RCONAddress == "" {
		return cfg, fmt.Errorf("MCBACKUP_RCON_ADDRESS is required")
	}
	if cfg.RCONPassword == "" {
		return cfg, fmt.Errorf("MCBACKUP_RCON_PASSWORD is required")
	}
	if cfg.WorldDir == "" {
		return cfg, fmt.Errorf("MCBACKUP_WORLD_DIR is required")
	}
	if cfg.BackupDir == "" {
		return cfg, fmt.Errorf("MCBACKUP_BACKUP_DIR is required")
	}
	return cfg, nil
}

func rconCommand(conn *rcon.Conn, command string, log *slog.Logger) error {
	log.Info("executing rcon", slog.String("command", command))
	response, executeErr := conn.Execute(command)
	if executeErr != nil {
		return executeErr
	}
	if response != "" {
		log.Info("rcon response", slog.String("command", command), slog.String("response", response))
	}
	return nil
}

func tarGz(srcDir string, dst string) (returnErr error) {
	out, createErr := os.Create(dst)
	if createErr != nil {
		return createErr
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil && returnErr == nil {
			returnErr = closeErr
		}
	}()
	gz := gzip.NewWriter(out)
	defer func() {
		if closeErr := gz.Close(); closeErr != nil && returnErr == nil {
			returnErr = closeErr
		}
	}()
	tw := tar.NewWriter(gz)
	defer func() {
		if closeErr := tw.Close(); closeErr != nil && returnErr == nil {
			returnErr = closeErr
		}
	}()
	parent := filepath.Dir(srcDir)
	walkErr := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		rel, relErr := filepath.Rel(parent, path)
		if relErr != nil {
			return relErr
		}
		header.Name = rel
		if writeHeaderErr := tw.WriteHeader(header); writeHeaderErr != nil {
			return writeHeaderErr
		}
		if d.IsDir() {
			return nil
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		return walkErr
	}
	return nil
}

func validateBackup(path string, log *slog.Logger) error {
	info, statErr := os.Stat(path)
	if statErr != nil {
		return statErr
	}
	if info.Size() == 0 {
		return fmt.Errorf("archive empty")
	}
	log.Info("archive validated", slog.Float64("size_mb", float64(info.Size())/1024/1024))
	return nil
}
