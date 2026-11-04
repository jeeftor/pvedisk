package session

import "testing"

func TestSELinuxConfigurationPrerequisites(t *testing.T) {
	for _, source := range []string{
		"SELINUX=enforcing\nSELINUXTYPE=targeted\n", "SELINUX=permissive\n",
		"# SELINUX=disabled\n SELINUX = enforcing # operator comment\n", "SELINUX='enforcing'\n",
		"SELINUX=\"permissive\"\r\n",
	} {
		if err := validateSELinuxConfig([]byte(source)); err != nil {
			t.Fatalf("rejected enabled SELinux config: %v", err)
		}
	}
	for _, source := range []string{
		"", "# SELINUX=enforcing\n", "SELINUX=disabled\n", "SELINUX=unknown\n",
		"SELINUX=enforcing\nSELINUX=disabled\n", "SELINUX=enforcing\nSELINUX=enforcing\n",
		"SELINUX=\"enforcing\n", "SELINUX=enforcing extra\n", "SELINUX=$(enable)\n", "SELINUX=enforcing\x00\n",
	} {
		if err := validateSELinuxConfig([]byte(source)); err == nil {
			t.Fatal("accepted disabled, absent, or ambiguous guest SELinux config")
		}
	}
}
