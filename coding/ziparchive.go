package coding

import (
	"archive/zip"
	"os"
	"time"
)

// Port of utils/zip.ts: the small classic ZIP archives used by bug reports.
// Go's archive/zip writes the same deflate-backed archive layout.

// WriteBugReportArchive writes the bundle files as a zip archive.
func WriteBugReportArchive(bundle BugReportBundle, filePath string) error {
	files := BugReportFiles(bundle)
	entries := make([]ZipEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, ZipEntry{Name: file.Name, Data: file.Data})
	}
	return WriteZipArchive(filePath, entries)
}

// ZipEntry is one archive member.
type ZipEntry struct {
	Name string
	Data string
}

// WriteZipArchive writes entries as a deflate-compressed zip archive.
func WriteZipArchive(filePath string, entries []ZipEntry) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	archive := zip.NewWriter(file)
	modified := time.Now()
	for _, entry := range entries {
		header := &zip.FileHeader{
			Name:     entry.Name,
			Method:   zip.Deflate,
			Modified: modified,
		}
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := writer.Write([]byte(entry.Data)); err != nil {
			return err
		}
	}
	return archive.Close()
}
