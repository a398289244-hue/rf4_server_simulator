package buy

import (
	"encoding/json"
	"os"
)

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
