package varsource

// ParseInline reads the variable sources of an inline playbook's text: its
// markers and vars_prompt entries. It has no repository, so its roles are
// "inline" (or "dynamic") and its imports "dynamic", each with a hint;
// nothing is gone because of them. The file name in sources and notes is
// "inline".
func ParseInline(text string) Playbook {
	if len(text) > MaxFileBytes {
		return failedPlaybook(inlinePath, FileUnreadable, readNote(inlinePath, errTooLarge))
	}
	d := decodePlaybook(inlinePath, []byte(text))
	if !d.ok {
		return failedPlaybook(inlinePath, FileUnreadable, d.notes...)
	}
	return (&parser{}).parse(inlinePath, d, len(text))
}
