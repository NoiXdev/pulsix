package repos

import "github.com/noixdev/pulsix/internal/varsource"

// checkVault drops from a report's vault names what no current worker
// sends and the store must not take: a name that is no variable name, a
// file longer than MaxStoredPathBytes, an entry past the caps. What is
// dropped may have named a secret, so the names are then Cut (not all
// known). It returns how many entries it dropped.
func checkVault(v *varsource.Vault) int {
	if v == nil {
		return 0
	}
	dropped := 0
	names := make([]varsource.VaultName, 0, len(v.Names))
	for _, n := range v.Names {
		if !varsource.ValidVaultName(n.Name) || len(n.File) > MaxStoredPathBytes || len(names) == varsource.MaxVaultNames {
			dropped++
			continue
		}
		names = append(names, n)
	}
	files := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, f := range list {
			if len(f) > MaxStoredPathBytes || len(out) == varsource.MaxVaultFiles {
				dropped++
				continue
			}
			out = append(out, f)
		}
		return out
	}
	v.Names, v.EncryptedFiles, v.Unread = names, files(v.EncryptedFiles), files(v.Unread)
	v.Cut = v.Cut || dropped > 0
	return dropped
}
