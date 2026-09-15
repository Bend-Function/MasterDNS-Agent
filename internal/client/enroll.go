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
	return persistEnrollment(configPath, cfg, enrollment, atomicWrite)
}

func persistEnrollment(configPath string, cfg config.Config, enrollment protocol.Enrollment, writeFile func(string, []byte, os.FileMode) error) error {
	if err := validateEnrollment(enrollment); err != nil {
		return err
	}
	pendingData, err := json.Marshal(enrollment)
	if err != nil {
		return errors.New("encode pending enrollment")
	}
	pendingPath := cfg.TokenFile + ".pending"
	if err := writeFile(pendingPath, append(pendingData, '\n'), 0o600); err != nil {
		return fmt.Errorf("write pending enrollment: %w", err)
	}

	cfg.ProbeID = enrollment.ProbeID
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode enrolled config: %w", err)
	}
	data = append(data, '\n')
	if err := writeFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("write enrolled config (pending enrollment retained): %w", err)
	}
	if err := writeFile(cfg.TokenFile, []byte(enrollment.RuntimeToken+"\n"), 0o600); err != nil {
		return fmt.Errorf("write runtime token (pending enrollment retained): %w", err)
	}
	if err := os.Remove(pendingPath); err != nil {
		return fmt.Errorf("remove pending enrollment: %w", err)
	}
	return nil
}

func RecoverEnrollment(configPath string, cfg config.Config) (bool, error) {
	pendingPath := cfg.TokenFile + ".pending"
	file, err := os.Open(pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open pending enrollment: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("pending enrollment must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return false, errors.New("pending enrollment must not be accessible by group or other users")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxInstallTokenBytes+1))
	if err != nil || len(data) > maxInstallTokenBytes {
		return false, errors.New("read pending enrollment")
	}
	var enrollment protocol.Enrollment
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&enrollment); err != nil {
		return false, errors.New("decode pending enrollment")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, errors.New("decode pending enrollment")
	}
	if err := file.Close(); err != nil {
		return false, errors.New("close pending enrollment")
	}
	if err := PersistEnrollment(configPath, cfg, enrollment); err != nil {
		return false, err
	}
	return true, nil
}

func validateEnrollment(enrollment protocol.Enrollment) error {
	if enrollment.Protocol != protocol.Version || !protocol.ValidID(enrollment.ProbeID) || enrollment.RuntimeToken == "" {
		return errors.New("invalid enrollment")
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
