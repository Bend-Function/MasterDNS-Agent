package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

const maxInstallTokenBytes = 16 << 10

func ReadInstallToken(path string, stdin io.Reader) (string, error) {
	reader := stdin
	var file *os.File
	if path != "" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open install token file: %w", err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return "", fmt.Errorf("stat install token file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("install token file must be a regular file")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("install token file must not be accessible by group or other users")
		}
		reader = file
	}
	if reader == nil {
		return "", errors.New("install token input is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxInstallTokenBytes+1))
	if err != nil {
		return "", errors.New("read install token")
	}
	if len(data) > maxInstallTokenBytes {
		return "", errors.New("install token exceeds size limit")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("install token is empty")
	}
	return token, nil
}

func PersistEnrollment(configPath string, cfg config.Config, enrollment protocol.Enrollment) error {
	if enrollment.Protocol != protocol.Version || enrollment.ProbeID == "" || enrollment.RuntimeToken == "" {
		return errors.New("invalid enrollment")
	}
	if err := atomicWrite(cfg.TokenFile, []byte(enrollment.RuntimeToken+"\n"), 0o600); err != nil {
		return fmt.Errorf("write runtime token: %w", err)
	}
	cfg.ProbeID = enrollment.ProbeID
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode enrolled config: %w", err)
	}
	data = append(data, '\n')
	if err := atomicWrite(configPath, data, 0o600); err != nil {
		return fmt.Errorf("write enrolled config: %w", err)
	}
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".masterdns-agent-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		temporary.Close()
		if err != nil {
			os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = io.Copy(temporary, bytes.NewReader(data)); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
