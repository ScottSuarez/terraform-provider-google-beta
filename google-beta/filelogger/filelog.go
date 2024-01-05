package filelogger

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type FileLog struct {
	baseDir string
}

// NewFileLog creates a new filelog instance with the given base directory
func NewFileLogger(baseDir string) *FileLog {

	return &FileLog{baseDir: baseDir}
}

// Clear all logs from the specified directory
func (fl *FileLog) ClearLogs() {
	dirPath := filepath.Join(fl.baseDir)
	err := os.RemoveAll(dirPath)
	if err != nil {
		log.Fatalf("Failed to clear logs for %s: %v", fl.baseDir, err)
	}
}

// Ensure the specified directory exists
func (fl *FileLog) ensureDir(dirPath string) {
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		err := os.MkdirAll(dirPath, 0755)
		if err != nil {
			log.Fatalf("Failed to create directory: %v", err)
		}
	}
}

// LogData logs to a file in the specified directory based on the schema type, file name, and data
func (fl *FileLog) LogData(subFolder, fileName, data string) {
	dirPath := filepath.Join(fl.baseDir, "schema", subFolder)
	fl.ensureDir(dirPath)

	filePath := filepath.Join(dirPath, fileName)

	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	_, err = fmt.Fprintln(file, data)
	if err != nil {
		log.Fatalf("Failed to write to log file: %v", err)
	}
}
