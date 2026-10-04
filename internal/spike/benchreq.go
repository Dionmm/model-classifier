package spike

// BenchQuestions approximates the production request shape: one tier Choice
// plus two Nouls. The wording is a placeholder, not tuned criteria.
func BenchQuestions() map[string]any {
	return map[string]any{
		"tier": map[string]any{
			"type":         "choice",
			"instructions": "Which model tier does the subagent task in `prompt` need?",
			"criteria": map[string]any{
				"fast":     "Mechanical or lookup work: search, read, list, run a command, summarise a small input.",
				"balanced": "Ordinary implementation or analysis with clear requirements.",
				"deep":     "Design, planning, review, debugging an unknown cause, or subtle multi-step reasoning.",
			},
		},
		"long_context": map[string]any{
			"type":         "noul",
			"instructions": "The task requires reading or holding a very large amount of material at once.",
		},
		"needs_vision": map[string]any{
			"type":         "noul",
			"instructions": "The task requires looking at images, screenshots or diagrams.",
		},
	}
}

// BenchState builds the Jev state from spawn args.
func BenchState(args map[string]any) map[string]any {
	str := func(k string) string {
		s, _ := args[k].(string)
		return s
	}
	st := ""
	for _, k := range subagentTypeKeys {
		if st = str(k); st != "" {
			break
		}
	}
	return map[string]any{
		"subagent_type": st,
		"description":   str("description"),
		"prompt":        str("prompt"),
	}
}
