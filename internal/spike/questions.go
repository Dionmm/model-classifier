package spike

import (
	"io"

	"github.com/Dionmm/model-classifier/internal/jev"
)

func LoadQuestions(r io.Reader) (map[string]any, error) { return jev.LoadQuestions(r) }
