package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONB is a map of strings to interfaces
type JSONB map[string]any

// Value implements the driver.Valuer interface
func (j JSONB) Value() (driver.Value, error) {
	valueString, err := json.Marshal(j)
	return string(valueString), err
}

// Scan implements the sql.Scanner interface. It accepts string (sqlite
// TEXT), []byte (postgres JSONB) and NULL. Panicking here deadlocks the
// connection (database/sql still holds the rows lock), so never assert.
func (j *JSONB) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*j = nil
		return nil
	case string:
		return json.Unmarshal([]byte(v), j)
	case []byte:
		return json.Unmarshal(v, j)
	default:
		return fmt.Errorf("unsupported type for JSONB: %T", value)
	}
}
