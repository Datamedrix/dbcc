package sshtunnel

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func localUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("LOGNAME")
}

func secretKey(r SecretRequest) string {
	return r.Kind + "\x00" + r.Hop + "\x00" + r.Key
}

// ask answers r: first with an answer that worked in an earlier login of
// this tunnel (once per login, so a wrong one is asked for again), else
// by asking the user. Answers to "answer" questions (one-time codes) are
// never remembered.
func (hs *handshake) ask(r SecretRequest) (string, error) {
	tu := hs.tu
	k := secretKey(r)
	if r.Kind != "answer" {
		hs.mu.Lock()
		tried := hs.tried[k]
		hs.tried[k] = true
		hs.mu.Unlock()
		tu.mu.Lock()
		s, ok := tu.secrets[k]
		tu.mu.Unlock()
		if ok && !tried {
			hs.remember(r, s)
			return s, nil
		}
	}
	if tu.cb.Secret == nil {
		return "", errors.New("no prompt available")
	}
	var s string
	var err error
	hs.userWait(func() { s, err = tu.cb.Secret(r) })
	if err != nil {
		return "", err
	}
	if r.Kind != "answer" {
		hs.remember(r, s)
	}
	return s, nil
}

// remember keeps an answer of this login (committed to the tunnel when the
// login succeeds) and, for the target, as the secret that was used.
func (hs *handshake) remember(r SecretRequest, s string) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.pending[secretKey(r)] = s
	if hs.isTarget {
		hs.used = &Secret{Kind: r.Kind, Key: r.Key, Value: s}
	}
}

func (hs *handshake) authMethods(user string) []ssh.AuthMethod {
	h := hs.hop
	who := user + "@" + h.Host
	if h.Port != 0 && h.Port != 22 {
		who = user + "@" + h.Addr()
	}
	req := func(kind, prompt string) SecretRequest {
		return SecretRequest{Kind: kind, Hop: h.Addr(), Target: hs.isTarget, Prompt: prompt}
	}
	return []ssh.AuthMethod{
		ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { return hs.signers(), nil }),
		ssh.PasswordCallback(func() (string, error) {
			return hs.ask(req("password", "SSH password for "+who+": "))
		}),
		ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i, q := range questions {
				q = strings.TrimSpace(q)
				kind := "answer"
				// The (only) hidden question that asks for a password, e.g.
				// "Password:" or "Password for x:"; anything else (one-time
				// codes) is never remembered.
				if len(questions) == 1 && !echos[0] && strings.Contains(strings.ToLower(q), "password") {
					kind, q = "password", "SSH password for "+who+":"
				} else if q == "" {
					q = "SSH " + who + ":"
				}
				a, err := hs.ask(req(kind, q+" "))
				if err != nil {
					return nil, err
				}
				answers[i] = a
			}
			return answers, nil
		}),
	}
}

// signers returns the keys to offer: configured identity files first
// (the agent's copy when it has one), then the remaining agent keys
// (unless IdentitiesOnly). Encrypted keys ask for their passphrase only
// when the server accepts the key.
func (hs *handshake) signers() []ssh.Signer {
	h := hs.hop
	var agentSigners []ssh.Signer
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			// Open for signing during this login; connect closes it.
			hs.mu.Lock()
			hs.agents = append(hs.agents, conn)
			hs.mu.Unlock()
			if s, err := agent.NewClient(conn).Signers(); err == nil {
				agentSigners = s
			}
		}
	}
	files := h.IdentityFiles
	if len(files) == 0 {
		home, _ := os.UserHomeDir()
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			files = append(files, filepath.Join(home, ".ssh", n))
		}
	}
	rsaAlgos := pubkeyAlgos(h.PubkeyAccepted)
	var out []ssh.Signer
	used := map[string]bool{}
	add := func(s ssh.Signer) {
		k := string(s.PublicKey().Marshal())
		if used[k] {
			return
		}
		used[k] = true
		if ws, ok := withAlgos(s, rsaAlgos); ok {
			out = append(out, ws)
		}
	}
	for _, f := range files {
		pub := readPublicKey(f)
		if pub != nil {
			if i := slices.IndexFunc(agentSigners, func(s ssh.Signer) bool {
				return string(s.PublicKey().Marshal()) == string(pub.Marshal())
			}); i >= 0 {
				add(agentSigners[i])
				continue
			}
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s, err := ssh.ParsePrivateKey(data)
		var pm *ssh.PassphraseMissingError
		switch {
		case err == nil:
			add(s)
		case errors.As(err, &pm):
			if pub == nil {
				pub = pm.PublicKey
			}
			if pub != nil {
				add(&lazySigner{pub: pub, file: f, data: data, hs: hs})
			}
		}
	}
	if !h.IdentitiesOnly {
		for _, s := range agentSigners {
			add(s)
		}
	}
	return out
}

func readPublicKey(file string) ssh.PublicKey {
	data, err := os.ReadFile(file + ".pub")
	if err != nil {
		return nil
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil
	}
	return k
}

// pubkeyAlgos returns the RSA signature algorithms to offer for
// PubkeyAcceptedAlgorithms (nil: library default).
func pubkeyAlgos(spec string) []string {
	if spec == "" {
		return nil
	}
	def := []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	all := []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	if l := applyAlgoSpec(def, all, spec); l != nil {
		return l
	}
	return []string{} // the setting names no RSA algorithm (not "default")
}

// withAlgos restricts or extends the algorithms of an RSA signer. Without
// a PubkeyAcceptedAlgorithms setting, RSA keys sign with SHA-2 only (as
// OpenSSH 8.8+ does); "+ssh-rsa" allows SHA-1 for old servers. ok is
// false when the setting accepts no RSA algorithm: the key is not offered.
func withAlgos(s ssh.Signer, algos []string) (ssh.Signer, bool) {
	if s.PublicKey().Type() != ssh.KeyAlgoRSA {
		return s, true
	}
	if algos == nil {
		algos = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	var rsa []string
	for _, a := range algos {
		if strings.HasPrefix(a, "rsa-") || a == ssh.KeyAlgoRSA {
			rsa = append(rsa, a)
		}
	}
	if len(rsa) == 0 {
		return nil, false
	}
	as, ok := s.(ssh.AlgorithmSigner)
	if !ok {
		return s, true
	}
	ms, err := ssh.NewSignerWithAlgorithms(as, rsa)
	if err != nil {
		return s, true
	}
	return ms, true
}

// lazySigner is an encrypted key file: it asks for the passphrase when the
// server wants a signature (that is, after it accepted the public key).
type lazySigner struct {
	pub  ssh.PublicKey
	file string
	data []byte
	hs   *handshake

	mu     sync.Mutex
	signer ssh.Signer
}

func (l *lazySigner) PublicKey() ssh.PublicKey { return l.pub }

func (l *lazySigner) unlock() (ssh.Signer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.signer != nil {
		return l.signer, nil
	}
	r := SecretRequest{Kind: "passphrase", Hop: l.hs.hop.Addr(), Key: l.file, Target: l.hs.isTarget,
		Prompt: "Passphrase for " + l.file + ": "}
	for try := 0; try < 3; try++ {
		pass, err := l.hs.ask(r)
		if err != nil {
			return nil, err
		}
		s, err := ssh.ParsePrivateKeyWithPassphrase(l.data, []byte(pass))
		if err == nil {
			l.signer = s
			return s, nil
		}
		if !errors.Is(err, x509.IncorrectPasswordError) && !strings.Contains(err.Error(), "incorrect") && !strings.Contains(err.Error(), "decrypt") {
			return nil, err
		}
		// Wrong: forget it and ask again.
		l.hs.mu.Lock()
		delete(l.hs.pending, secretKey(r))
		if l.hs.isTarget {
			l.hs.used = nil
		}
		l.hs.mu.Unlock()
	}
	return nil, fmt.Errorf("key %s: wrong passphrase", l.file)
}

func (l *lazySigner) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	s, err := l.unlock()
	if err != nil {
		return nil, err
	}
	return s.Sign(rand, data)
}

func (l *lazySigner) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	s, err := l.unlock()
	if err != nil {
		return nil, err
	}
	if as, ok := s.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(rand, data, algorithm)
	}
	if algorithm != "" && algorithm != s.PublicKey().Type() {
		return nil, fmt.Errorf("key %s cannot sign with %s", l.file, algorithm)
	}
	return s.Sign(rand, data)
}
