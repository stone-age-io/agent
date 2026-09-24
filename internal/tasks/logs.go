package tasks

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FetchLogLines reads the last N lines from a log file
// Only files matching allowed patterns can be read
func (e *Executor) FetchLogLines(logPath string, lines int, allowedPatterns []string) ([]string, error) {
	// Validate path is allowed
	if !isPathAllowed(logPath, allowedPatterns) {
		return nil, fmt.Errorf("log path not in allowed list: %s", logPath)
	}

	// Validate lines parameter
	if lines <= 0 {
		return nil, fmt.Errorf("lines must be greater than 0")
	}
	if lines > 10000 {
		return nil, fmt.Errorf("lines cannot exceed 10000")
	}

	// Read the file
	return tailFile(logPath, lines)
}

// isPathAllowed reports whether a requested path is one of the files an
// allowed pattern names today: the patterns are expanded with filepath.Glob
// and the cleaned request must equal one of the results exactly.
//
// THE ALLOWLIST IS THE WHOLE CHECK. Exact equality with a glob match already
// rules out traversal -- a cleaned absolute path holds no `..`, and it must be
// a file the operator's pattern names. There used to be a substring denylist
// in front of this ("sam", "system32", ".exe", "..") and a pattern-prefix
// check behind it. Neither refused anything the equality allowed; what they
// did refuse was /var/log/samba/*, any path under a user called Samuel, and
// every pattern using `?` or `[...]`, because the prefix check only knew `*`.
func isPathAllowed(requestedPath string, allowedPatterns []string) bool {
	cleanPath := filepath.Clean(requestedPath)
	if !filepath.IsAbs(cleanPath) {
		return false
	}

	for _, pattern := range allowedPatterns {
		matches, err := filepath.Glob(filepath.Clean(pattern))
		if err != nil {
			continue
		}
		for _, match := range matches {
			if cleanPath == filepath.Clean(match) {
				return true
			}
		}
	}
	return false
}

// tailFile reads the last N lines from a file
func tailFile(filePath string, n int) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	fileSize := stat.Size()

	// If file is small, just read all lines
	if fileSize < 1024*1024 { // Less than 1MB
		return readAllLines(file, n)
	}

	// For larger files, use a more efficient approach
	// Start from the end and read backwards
	return readLastNLines(file, fileSize, n)
}

// readAllLines reads all lines and returns the last N
func readAllLines(file *os.File, n int) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	// Return last N lines
	if len(lines) <= n {
		return lines, nil
	}

	return lines[len(lines)-n:], nil
}

// readLastNLines efficiently reads the last N lines from a large file
func readLastNLines(file *os.File, fileSize int64, n int) ([]string, error) {
	const bufferSize = 4096
	buffer := make([]byte, bufferSize)
	// Collect lines in reverse order (last line first), then reverse at the end
	var lines []string
	var currentLine strings.Builder

	// Start from the end of the file
	pos := fileSize

	for len(lines) < n && pos > 0 {
		// Calculate how much to read
		readSize := int64(bufferSize)
		if pos < readSize {
			readSize = pos
		}

		pos -= readSize

		// Read chunk
		_, err := file.ReadAt(buffer[:readSize], pos)
		if err != nil {
			return nil, fmt.Errorf("error reading file: %w", err)
		}

		// Process buffer backwards
		for i := int(readSize) - 1; i >= 0; i-- {
			if buffer[i] == '\n' {
				if currentLine.Len() > 0 {
					lines = append(lines, reverseString(currentLine.String()))
					currentLine.Reset()
				}
				if len(lines) >= n {
					break
				}
			} else if buffer[i] != '\r' {
				// Build character backwards (we're scanning right-to-left)
				currentLine.WriteByte(buffer[i])
			}
		}
	}

	// Add any remaining content as the first line
	if currentLine.Len() > 0 && len(lines) < n {
		lines = append(lines, reverseString(currentLine.String()))
	}

	// Reverse the slice so lines are in file order (first to last)
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}

	return lines, nil
}

// reverseString reverses a string
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
