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

// isPathAllowed checks if a requested path matches any of the allowed patterns
// Enhanced with additional security checks
func isPathAllowed(requestedPath string, allowedPatterns []string) bool {
	// Clean and normalize the path to prevent path traversal
	cleanPath := filepath.Clean(requestedPath)
	
	// Reject paths with suspicious patterns
	// Convert to lowercase for case-insensitive comparison on Windows
	lowerPath := strings.ToLower(cleanPath)
	
	// Reject paths containing suspicious elements
	suspiciousPatterns := []string{
		"..",           // Parent directory traversal
		"system32",     // System directories
		"\\windows\\",  // Windows directory
		"\\program files\\", // Program Files
		"sam",          // Security Account Manager
		".exe",         // Executables
		".dll",         // Libraries
		".sys",         // System files
	}
	
	for _, suspicious := range suspiciousPatterns {
		if strings.Contains(lowerPath, suspicious) {
			return false
		}
	}
	
	// Ensure path is absolute (starts with drive letter on Windows)
	if !filepath.IsAbs(cleanPath) {
		return false
	}

	// Check against allowed patterns
	for _, pattern := range allowedPatterns {
		// Clean the pattern too
		cleanPattern := filepath.Clean(pattern)
		
		// Expand glob pattern
		matches, err := filepath.Glob(cleanPattern)
		if err != nil {
			continue
		}

		// Check if requested path matches any expanded path
		for _, match := range matches {
			cleanMatch := filepath.Clean(match)
			if cleanPath == cleanMatch {
				// Additional check: ensure the matched path is still within expected directories
				// Extract the base directory from the pattern (before any wildcards)
				patternBase := strings.Split(cleanPattern, "*")[0]
				patternBase = filepath.Clean(patternBase)
				
				// Ensure the matched file is within the pattern's base directory
				if strings.HasPrefix(cleanPath, patternBase) {
					return true
				}
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
					lines = append([]string{currentLine.String()}, lines...)
					currentLine.Reset()
				}
				if len(lines) >= n {
					break
				}
			} else if buffer[i] != '\r' {
				// Prepend character (we're going backwards)
				currentLine.WriteByte(buffer[i])
			}
		}
	}

	// Add any remaining content as the first line
	if currentLine.Len() > 0 {
		// Reverse the string since we built it backwards
		line := currentLine.String()
		reversed := reverseString(line)
		lines = append([]string{reversed}, lines...)
	}

	// Reverse each line (since we built them backwards)
	for i := range lines {
		if i == 0 && currentLine.Len() > 0 {
			continue // First line already reversed
		}
		lines[i] = reverseString(lines[i])
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
