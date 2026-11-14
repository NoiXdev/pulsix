package script

// unixPath is a command's PATH on Unix.
const unixPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// windowsVars are copied into a command's environment on Windows when the
// agent's own environment sets them.
var windowsVars = []string{"SystemRoot", "windir", "SystemDrive", "ComSpec", "PATHEXT", "TEMP", "TMP"}

// commandEnv is the environment a command runs with on goos. It is built,
// never inherited, so the agent's token and secrets never reach a script:
// on Unix a fixed PATH and the C locale; on Windows the system's own
// locations, read through lookup from the agent's environment, and a PATH
// of the system directories (from SystemRoot, or windir without it).
func commandEnv(goos string, lookup func(string) (string, bool)) []string {
	if goos != "windows" {
		return []string{"PATH=" + unixPath, "LANG=C", "LC_ALL=C"}
	}
	env := make([]string, 0, len(windowsVars)+1)
	for _, name := range windowsVars {
		if v, ok := lookup(name); ok {
			env = append(env, name+"="+v)
		}
	}
	root, ok := lookup("SystemRoot")
	if !ok || root == "" {
		root, ok = lookup("windir")
	}
	if ok && root != "" {
		env = append(env, `PATH=`+root+`\System32;`+root+`;`+root+`\System32\WindowsPowerShell\v1.0`)
	}
	return env
}
