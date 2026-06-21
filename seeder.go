// Expected Excel column order for ConstructManifestFromColumns:
// Column 0: Entity ID
// Column 1: Tier ("verified" | "unverified" | "knowledge")
// Column 2: Name (Degree 1)
// Column 3: Email (Degree 2)
// Column 4: Phone (Degree 2)
// Column 5: Type/Category (Degree 1)

package fusiondb

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/xuri/excelize/v2"
	"gopkg.in/yaml.v3"
)

const SeederBatchSize = 100 // Commit every 100 entities to prevent txn size overflow

// Original functions
func ParseMarkdownManifest(path string) (*UFLManifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(string(content), "---")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid frontmatter in %s", path)
	}

	var manifest UFLManifest
	if err := yaml.Unmarshal([]byte(parts[1]), &manifest); err != nil {
		return nil, err
	}
	if manifest.Entity.KV == nil {
		manifest.Entity.KV = make(map[string]any)
	}
	manifest.Entity.KV["description"] = strings.TrimSpace(parts[2])
	manifest.Action = "fuse"
	return &manifest, nil
}

func ParseExcelManifests(path string) ([]UFLManifest, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rows, err := f.GetRows("Entities")
	if err != nil {
		return nil, err
	}

	if len(rows) < 1 {
		return nil, fmt.Errorf("empty Entities sheet")
	}
	headers := rows[0]
	var manifests []UFLManifest
	for _, row := range rows[1:] {
		m := UFLManifest{Action: "fuse", Entity: UFLEntity{KV: make(map[string]any)}}
		for i, val := range row {
			if i >= len(headers) {
				break
			}
			header := headers[i]
			switch header {
			case "_id":
				m.Entity.ID = val
			case "_type":
				m.Entity.Type = val
			case "_tier":
				m.Entity.Tier = val
			default:
				m.Entity.KV[header] = val
			}
		}
		if m.Entity.ID != "" {
			manifests = append(manifests, m)
		}
	}
	return manifests, nil
}

// New Streaming functions for Module 3
func ConstructManifestFromColumns(columns []string) UFLManifestPII {
	m := UFLManifestPII{
		Entity: UFLEntityPII{
			KV: make(map[string]string),
		},
	}
	if len(columns) > 0 {
		m.Entity.ID = columns[0]
	}
	if len(columns) > 1 {
		m.Entity.Tier = columns[1]
	}
	if len(columns) > 2 {
		m.Entity.KV["name"] = columns[2]
		m.Entity.Relations = append(m.Entity.Relations, UFLRelationPII{
			Predicate: "has_name",
			Object:    columns[2],
			Degree:    1,
		})
	}
	if len(columns) > 3 && columns[3] != "" {
		m.Entity.KV["email"] = columns[3]
		m.Entity.Relations = append(m.Entity.Relations, UFLRelationPII{
			Predicate: "has_email",
			Object:    columns[3],
			Degree:    2,
		})
	}
	if len(columns) > 4 && columns[4] != "" {
		m.Entity.KV["phone"] = columns[4]
		m.Entity.Relations = append(m.Entity.Relations, UFLRelationPII{
			Predicate: "has_phone",
			Object:    columns[4],
			Degree:    2,
		})
	}
	if len(columns) > 5 && columns[5] != "" {
		m.Entity.KV["category"] = columns[5]
		m.Entity.Relations = append(m.Entity.Relations, UFLRelationPII{
			Predicate: "is_a",
			Object:    columns[5],
			Degree:    1,
		})
	}
	return m
}

func readLine(r *bufio.Reader) (string, error) {
	var line []byte
	for {
		l, isPrefix, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		line = append(line, l...)
		if !isPrefix {
			break
		}
	}
	return string(line), nil
}

func StreamRemainingBody(r io.Reader) io.Reader {
	return r
}

func parseFrontmatterAndStreamBody(filePath string) (UFLManifestPII, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return UFLManifestPII{}, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	// Read first line, must be "---"
	line, err := readLine(reader)
	if err != nil {
		return UFLManifestPII{}, err
	}
	if strings.TrimSpace(line) != "---" {
		return UFLManifestPII{}, fmt.Errorf("invalid markdown: missing opening frontmatter boundary")
	}

	var frontmatterLines []string
	for {
		line, err = readLine(reader)
		if err != nil {
			return UFLManifestPII{}, fmt.Errorf("invalid markdown: missing closing frontmatter boundary: %w", err)
		}
		if strings.TrimSpace(line) == "---" {
			break
		}
		frontmatterLines = append(frontmatterLines, line)
	}

	var parsed struct {
		Entity struct {
			ID   string            `yaml:"id"`
			Tier string            `yaml:"tier"`
			Type string            `yaml:"type"`
			KV   map[string]string `yaml:"kv"`
		} `yaml:"entity"`
	}

	frontmatterText := strings.Join(frontmatterLines, "\n")
	if err := yaml.Unmarshal([]byte(frontmatterText), &parsed); err != nil {
		return UFLManifestPII{}, err
	}

	m := UFLManifestPII{
		Entity: UFLEntityPII{
			ID:   parsed.Entity.ID,
			Tier: parsed.Entity.Tier,
			KV:   parsed.Entity.KV,
		},
	}
	if m.Entity.KV == nil {
		m.Entity.KV = make(map[string]string)
	}
	if parsed.Entity.Type != "" {
		m.Entity.Relations = append(m.Entity.Relations, UFLRelationPII{
			Predicate: "is_a",
			Object:    parsed.Entity.Type,
			Degree:    1,
		})
	}

	// Stream remaining body
	bodyReader := StreamRemainingBody(reader)
	bodyBytes, err := io.ReadAll(bodyReader)
	if err != nil {
		return UFLManifestPII{}, err
	}
	m.Entity.KV["description"] = strings.TrimSpace(string(bodyBytes))

	return m, nil
}

func StreamDirectorySeeder(db *DB, sourceDir string, secretKey, saltKey []byte) error {
	var txn *badger.Txn
	txn = db.core.NewTransaction(true)
	defer func() {
		if txn != nil {
			txn.Discard()
		}
	}()

	batchCount := 0

	err := filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		if strings.HasSuffix(d.Name(), ".xlsx") {
			excelFile, err := excelize.OpenFile(path)
			if err != nil {
				return err
			}
			defer excelFile.Close()

			rowStream, err := excelFile.Rows("Entities")
			if err != nil {
				return err
			}
			defer rowStream.Close()

			// Skip header
			if rowStream.Next() {
				_, _ = rowStream.Columns()
			}

			for rowStream.Next() {
				columns, _ := rowStream.Columns()
				if len(columns) == 0 {
					continue
				}
				manifest := ConstructManifestFromColumns(columns)
				if err := SecureFuseEntity(txn, manifest, secretKey, saltKey); err != nil {
					return err
				}
				batchCount++
				if batchCount >= SeederBatchSize {
					if err := txn.Commit(); err != nil {
						return err
					}
					txn = db.core.NewTransaction(true)
					batchCount = 0
				}
			}
		} else if strings.HasSuffix(d.Name(), ".md") {
			manifest, err := parseFrontmatterAndStreamBody(path)
			if err != nil {
				return err
			}

			if err := SecureFuseEntity(txn, manifest, secretKey, saltKey); err != nil {
				return err
			}
			batchCount++
			if batchCount >= SeederBatchSize {
				if err := txn.Commit(); err != nil {
					return err
				}
				txn = db.core.NewTransaction(true)
				batchCount = 0
			}
		}
		return nil
	})

	if err != nil {
		return err
	}

	if batchCount > 0 {
		if err := txn.Commit(); err != nil {
			return err
		}
		txn = nil
	}

	return nil
}
