package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/spf13/cobra"
)

const testShadowHash = "$y$j9T$testsalt$encoded-test-password"

func shadowFixture(t *testing.T, contents string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "shadow")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func shadowEntry(user, hash string) string { return user + ":" + hash + ":20000:0:99999:7:::" }

func executeUtility(ops utilityOperations, args ...string) (string, error) {
	command := newUtilsCommand(ops)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetIn(strings.NewReader(""))
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}

func TestUtilitiesDefaultToHelpWithoutConfiguration(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) {
		t.Fatal("configuration loaded for utility")
		return config.Config{}, nil
	}
	output, err := execute(ops, "--config", "/missing/invalid.yaml", "utils")
	if err != nil || !strings.Contains(output, "hash-password") || !strings.Contains(output, "verify-password") {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestHashPasswordPrintsOnlyHashAndClearsSecret(t *testing.T) {
	secret := []byte("known-password")
	prompts := 0
	ops := utilityOperations{
		prompt: func(_ *cobra.Command, confirm bool) ([]byte, error) {
			prompts++
			if !confirm {
				t.Fatal("hash password did not request confirmation")
			}
			return secret, nil
		},
		hash: func(value []byte) (string, error) {
			if string(value) != "known-password" {
				t.Fatal("wrong password")
			}
			return testShadowHash, nil
		},
	}
	output, err := executeUtility(ops, "hash-password")
	if err != nil || prompts != 1 || output != testShadowHash+"\n" {
		t.Fatalf("prompts=%d output=%q err=%v", prompts, output, err)
	}
	assertCleared(t, secret)
}

func TestVerifyPasswordChecksOneAccountAndOnePassword(t *testing.T) {
	for _, match := range []bool{true, false} {
		t.Run(map[bool]string{true: "match", false: "mismatch"}[match], func(t *testing.T) {
			contents := shadowEntry("root", "!") + "\n" + shadowEntry("operator", testShadowHash) + "\n"
			path := shadowFixture(t, contents)
			secret := []byte("known-password")
			prompts, checks := 0, 0
			ops := utilityOperations{
				prompt: func(_ *cobra.Command, confirm bool) ([]byte, error) {
					prompts++
					if confirm {
						t.Fatal("verify requested password confirmation")
					}
					return secret, nil
				},
				verify: func(value []byte, encoded string) (bool, error) {
					checks++
					if string(value) != "known-password" || encoded != testShadowHash {
						t.Fatal("wrong verification input")
					}
					return match, nil
				},
			}
			output, err := executeUtility(ops, "verify-password", "--shadow", path, "--user", "operator")
			if prompts != 1 || checks != 1 || (err == nil) != match {
				t.Fatalf("prompts=%d checks=%d err=%v", prompts, checks, err)
			}
			if strings.Contains(output, testShadowHash) || strings.Contains(output, "known-password") {
				t.Fatal("verification disclosed password or hash")
			}
			if match && !strings.Contains(output, "matches the shadow entry for operator") {
				t.Fatalf("missing match result: %q", output)
			}
			if !match && (!strings.Contains(err.Error(), "does not match") || strings.Contains(output, "matches")) {
				t.Fatalf("false match: output=%q err=%v", output, err)
			}
			assertCleared(t, secret)
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != contents {
				t.Fatal("verification changed the shadow file")
			}
		})
	}
}

func TestInvalidShadowInputDoesNotPrompt(t *testing.T) {
	for _, test := range []struct{ name, contents, user string }{
		{"malformed", "root:hash", "root"},
		{"duplicate", shadowEntry("root", testShadowHash) + "\n" + shadowEntry("root", testShadowHash), "root"},
		{"locked", shadowEntry("root", "!"+testShadowHash), "root"},
		{"disabled", shadowEntry("root", "*"), "root"},
		{"empty", shadowEntry("root", ""), "root"},
		{"missing-account", shadowEntry("other", testShadowHash), "root"},
		{"zero-byte", shadowEntry("root", testShadowHash) + "\x00", "root"},
		{"oversize", strings.Repeat("a", maxShadowSize+1), "root"},
		{"invalid-user", shadowEntry("root", testShadowHash), "root\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := shadowFixture(t, test.contents)
			ops := utilityOperations{prompt: func(*cobra.Command, bool) ([]byte, error) {
				t.Fatal("prompted on invalid shadow input")
				return nil, nil
			}}
			output, err := executeUtility(ops, "verify-password", "--shadow", path, "--user", test.user)
			if err == nil || strings.Contains(err.Error(), testShadowHash) || strings.Contains(output, testShadowHash) {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
}

func TestShadowPathRejectsMissingRelativeDirectoryAndSymlinks(t *testing.T) {
	file := shadowFixture(t, shadowEntry("root", testShadowHash))
	root := filepath.Dir(file)
	link := filepath.Join(root, "shadow-link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	dirlink := filepath.Join(root, "directory-link")
	if err := os.Symlink(root, dirlink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "shadow", root, filepath.Join(root, "missing"), link, filepath.Join(dirlink, "shadow")} {
		if _, err := readShadowHash(path, "root"); err == nil {
			t.Fatalf("accepted unsafe shadow path %q", path)
		}
	}
}

func TestUtilityClearsPasswordOnFailure(t *testing.T) {
	path := shadowFixture(t, shadowEntry("root", testShadowHash))
	for _, action := range []string{"hash-password", "verify-password"} {
		for _, promptFailure := range []bool{true, false} {
			t.Run(action+map[bool]string{true: "-prompt-failure", false: "-crypt-failure"}[promptFailure], func(t *testing.T) {
				secret := []byte("known-password")
				expected := errors.New("password operation failed")
				ops := utilityOperations{
					prompt: func(*cobra.Command, bool) ([]byte, error) {
						if promptFailure {
							return secret, expected
						}
						return secret, nil
					},
					hash:   func([]byte) (string, error) { return "", expected },
					verify: func([]byte, string) (bool, error) { return false, expected },
				}
				args := []string{action}
				if action == "verify-password" {
					args = append(args, "--shadow", path)
				}
				output, err := executeUtility(ops, args...)
				if !errors.Is(err, expected) || output != "" {
					t.Fatalf("output=%q err=%v", output, err)
				}
				assertCleared(t, secret)
			})
		}
	}
}

func TestUtilitiesRejectPasswordArgumentsAndFlags(t *testing.T) {
	for _, args := range [][]string{{"hash-password", "plaintext"}, {"hash-password", "--password", "plaintext"}, {"verify-password", "plaintext"}} {
		ops := utilityOperations{prompt: func(*cobra.Command, bool) ([]byte, error) { t.Fatal("unexpected password prompt"); return nil, nil }}
		if output, err := executeUtility(ops, args...); err == nil {
			t.Fatalf("accepted plaintext arguments: %v", args)
		} else if strings.Contains(output, "plaintext") || strings.Contains(err.Error(), "plaintext") {
			t.Fatal("rejected password argument was disclosed in output")
		}
	}
}

func TestSecretPromptRequiresTerminal(t *testing.T) {
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("password\n"))
	if secret, err := readSecret(command, "Password: "); err == nil || len(secret) > 0 {
		t.Fatalf("read secret without terminal: err=%v", err)
	}
}

func assertCleared(t *testing.T, secret []byte) {
	t.Helper()
	for _, value := range secret {
		if value != 0 {
			t.Fatal("password bytes were retained")
		}
	}
}
