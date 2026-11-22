package ui

// F2: connection manager. Pick a saved connection, or enter connection
// data, test it and save it under a name (owner request 2026-11-21).
// Credentials are stored in the separate credential store (A22).

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"
	"dbcc/internal/sshtunnel"
)

var drivers = []struct{ name, label string }{
	{"mysql", "MySQL / MariaDB"},
	{"postgres", "PostgreSQL"},
	{"mssql", "SQL Server"},
}

var defaultPort = map[string]int{"mysql": 3306, "postgres": 5432, "mssql": 1433}

// Password storage choices in the editor.
var pwChoices = []struct{ source, label string }{
	{"stored", "save with the connection"},
	{"prompt", "ask when connecting"},
	{"env:", "environment variable"},
	{"mycnf", "~/.my.cnf"},
	{"pgpass", "~/.pgpass"},
}

func (a *App) configPath() string {
	if a.cfg.Path != "" {
		return a.cfg.Path
	}
	if a.opts.ConfigPath != "" {
		return a.opts.ConfigPath
	}
	return config.DefaultPath()
}

// showProfiles lists the saved connections. Enter connects (cd /name),
// n creates, e edits, c copies, d deletes, x closes an open connection.
func (a *App) showProfiles() {
	prev := a.tv.GetFocus()
	if name, _ := a.pages.GetFrontPage(); name == "profiles" {
		return
	}
	list := tview.NewList().ShowSecondaryText(true).SetHighlightFullLine(true).
		SetSecondaryTextColor(tcell.ColorGray)
	var names []string
	fill := func(selectName string) {
		list.Clear()
		names = names[:0]
		for n := range a.cfg.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		open := a.ws.Sessions()
		cur := a.ws.Path().Conn
		sel := 0
		for i, n := range names {
			p := a.cfg.Profiles[n]
			state := ""
			if _, ok := open[n]; ok {
				state = "  [green]● open[-]"
			}
			if n == cur {
				state = "  [green::b]● current[-::-]"
			}
			list.AddItem(fmt.Sprintf("[::b]%s[::-]%s", tview.Escape(n), state), "  "+tview.Escape(describeProfile(p)), 0, nil)
			if n == selectName {
				sel = i
			}
		}
		list.AddItem("[teal::b]+ New connection…[-::-]", "  enter host, user and password, test and save", 0, nil)
		list.SetCurrentItem(sel)
	}
	fill(a.ws.Path().Conn)
	hint := tview.NewTextView().SetDynamicColors(true).SetText(
		"[yellow]Enter[-] connect  [yellow]n[-] new  [yellow]e[-] edit  [yellow]c[-] copy  [yellow]d[-] delete  [yellow]x[-] disconnect  [yellow]Esc[-] close\n" +
			"[gray]saved in " + tview.Escape(a.configPath()) + " · passwords in " + tview.Escape(config.CredentialsPath()) + "[-]")
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(list, 0, 1, true).AddItem(hint, 2, 0, false)
	box.SetBorder(true).SetTitle(" Connections ").SetBorderColor(a.theme.BorderFocus).SetBorderPadding(0, 0, 1, 1)

	selected := func() (string, bool) {
		i := list.GetCurrentItem()
		if i < 0 || i >= len(names) {
			return "", false
		}
		return names[i], true
	}
	refresh := func(name string) { fill(name) }
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		name, isProfile := selected()
		r := ev.Rune()
		switch {
		case ev.Key() == tcell.KeyEscape:
			a.closeModal("profiles", prev)
			return nil
		case ev.Key() == tcell.KeyEnter:
			if !isProfile {
				a.editProfile(nil, false, refresh)
				return nil
			}
			a.closeModal("profiles", a.tree)
			a.invoke("cd", QuoteArg("/"+name))
			return nil
		case ev.Key() == tcell.KeyRune && r == 'n':
			a.editProfile(nil, false, refresh)
			return nil
		case ev.Key() == tcell.KeyRune && (r == 'e' || r == 'c') && isProfile:
			a.editProfile(a.cfg.Profiles[name], r == 'c', refresh)
			return nil
		case ev.Key() == tcell.KeyRune && r == 'd' && isProfile:
			a.confirmUI("Delete the connection "+name+" and its stored password?", func() {
				if s := a.ws.Sessions()[name]; s != nil {
					a.ws.Close(name)
					a.tree.Refresh()
				}
				if err := config.DeleteProfile(a.cfg, a.configPath(), name); err != nil {
					a.lastErr = err.Error()
				}
				refresh("")
				a.tv.SetFocus(list)
			})
			return nil
		case ev.Key() == tcell.KeyRune && r == 'x' && isProfile:
			a.disconnect(name, func() { refresh(name) })
			return nil
		}
		return ev
	})
	_, _, w, h := a.pages.GetRect()
	a.modal("profiles", box, min(max(w-20, 60), 100), min(max(len(names)*2+6, 12), max(h-6, 12)))
}

func describeProfile(p *config.Profile) string {
	where := p.Host
	if where == "" {
		where = "localhost"
	}
	if p.Socket != "" {
		where = p.Socket
	} else if p.Port != 0 {
		where += ":" + strconv.Itoa(p.Port)
	}
	d := p.Driver
	for _, x := range drivers {
		if x.name == p.Driver {
			d = x.label
		}
	}
	s := fmt.Sprintf("%s  %s@%s", d, p.User, where)
	if p.Database != "" {
		s += "/" + p.Database
	}
	var flags []string
	if p.ReadOnly {
		flags = append(flags, "read-only")
	}
	if !p.IsAutocommit() {
		flags = append(flags, "manual txn")
	}
	if p.ConfirmWrites {
		flags = append(flags, "confirm writes")
	}
	if len(flags) > 0 {
		s += "  (" + strings.Join(flags, ", ") + ")"
	}
	if p.SSH != nil {
		s += "  via SSH " + p.SSH.Name()
	}
	return s
}

// disconnect closes an open connection, asking about an open transaction.
func (a *App) disconnect(name string, done func()) {
	a.do(func() {
		s := a.ws.Sessions()[name]
		if s == nil {
			return
		}
		if s.TxnOpen() {
			if err := a.txnDecision(s, ""); err != nil {
				return
			}
		}
		if err := a.ws.Close(name); err != nil {
			a.message(exec.Message{Err: err})
		}
		a.meta.InvalidateAll(name)
		a.ui(func() {
			a.lastPath = meta.Path{Conn: "\x00"}
			a.tree.Refresh()
			a.updateStatus()
			if done != nil {
				done()
			}
		})
	})
}

// editProfile opens the connection editor. orig nil = new connection;
// copy = start from orig under a new name.
func (a *App) editProfile(orig *config.Profile, copy bool, saved func(name string)) {
	prev := a.tv.GetFocus()
	p := &config.Profile{Driver: "mysql", Host: "127.0.0.1", Port: 3306, Password: "stored"}
	editing := orig != nil && !copy
	if orig != nil {
		cp := *orig
		p = &cp
		if copy {
			p.Name = orig.Name + "-copy"
		}
	}
	hasStored := false
	if editing {
		_, hasStored, _ = config.StoredPassword(orig.Name)
	}

	f := tview.NewForm()
	a.profileForm = f
	f.SetItemPadding(0).SetButtonsAlign(tview.AlignLeft)
	f.SetFieldBackgroundColor(tcell.ColorDefault).SetLabelColor(a.theme.Accent)
	f.SetBorder(true).SetBorderColor(a.theme.BorderFocus).SetBorderPadding(0, 0, 1, 1)
	if editing {
		f.SetTitle(" Edit connection " + p.Name + " ")
	} else {
		f.SetTitle(" New connection ")
	}
	const w = 40
	name := tview.NewInputField().SetLabel("Name").SetText(p.Name).SetFieldWidth(w)
	driverIdx := 0
	var driverLabels []string
	for i, d := range drivers {
		driverLabels = append(driverLabels, d.label)
		if d.name == p.Driver {
			driverIdx = i
		}
	}
	driver := tview.NewDropDown().SetLabel("Database").SetOptions(driverLabels, nil).SetCurrentOption(driverIdx)
	host := tview.NewInputField().SetLabel("Host").SetText(p.Host).SetFieldWidth(w)
	port := tview.NewInputField().SetLabel("Port").SetFieldWidth(8).SetAcceptanceFunc(tview.InputFieldInteger)
	if p.Port != 0 {
		port.SetText(strconv.Itoa(p.Port))
	}
	socket := tview.NewInputField().SetLabel("Socket (optional)").SetText(p.Socket).SetFieldWidth(w)
	user := tview.NewInputField().SetLabel("User").SetText(p.User).SetFieldWidth(w)
	pw := tview.NewInputField().SetLabel("Password").SetMaskCharacter('*').SetFieldWidth(w)
	if hasStored {
		pw.SetPlaceholder("(saved — leave empty to keep)").SetPlaceholderTextColor(tcell.ColorGray)
	}
	src := p.Password
	if src == "" {
		src = "prompt"
	}
	pwIdx := 0
	envName := ""
	var pwLabels []string
	for i, c := range pwChoices {
		pwLabels = append(pwLabels, c.label)
		if c.source == src || (c.source == "env:" && strings.HasPrefix(src, "env:")) {
			pwIdx = i
		}
	}
	if strings.HasPrefix(src, "env:") {
		envName = strings.TrimPrefix(src, "env:")
	}
	pwSource := tview.NewDropDown().SetLabel("Password storage").SetOptions(pwLabels, nil).SetCurrentOption(pwIdx)
	env := tview.NewInputField().SetLabel("Env variable").SetText(envName).SetFieldWidth(w).
		SetPlaceholder("only for 'environment variable'").SetPlaceholderTextColor(tcell.ColorGray)
	database := tview.NewInputField().SetLabel("Default database").SetText(p.Database).SetFieldWidth(w)
	readOnly := tview.NewCheckbox().SetLabel("Read only").SetChecked(p.ReadOnly)
	autocommit := tview.NewCheckbox().SetLabel("Autocommit").SetChecked(p.IsAutocommit())
	confirm := tview.NewCheckbox().SetLabel("Confirm writes").SetChecked(p.ConfirmWrites)
	var params []string
	for k, v := range p.Params {
		params = append(params, k+"="+v)
	}
	sort.Strings(params)
	paramsField := tview.NewInputField().SetLabel("Driver options").SetText(strings.Join(params, ", ")).SetFieldWidth(w).
		SetPlaceholder("e.g. tls=skip-verify, sslmode=require").SetPlaceholderTextColor(tcell.ColorGray)
	driver.SetSelectedFunc(func(_ string, i int) {
		cur, _ := strconv.Atoi(port.GetText())
		for _, d := range drivers {
			if cur == defaultPort[d.name] || port.GetText() == "" {
				port.SetText(strconv.Itoa(defaultPort[drivers[i].name]))
				break
			}
		}
	})
	// SSH tunnel (A27): a Host of ~/.ssh/config or manual data.
	sshCfg, sshCfgErr := sshtunnel.LoadConfig(session.SSHConfigPath())
	var aliases []string
	if sshCfgErr == nil {
		aliases = sshCfg.Hosts()
	}
	tunnelLabels := []string{"none (direct connection)", "manual (enter below)"}
	for _, al := range aliases {
		lbl := al
		if t, err := sshCfg.Resolve(al); err == nil {
			who := t.Addr()
			if t.User != "" {
				who = t.User + "@" + who
			}
			lbl += "  (" + who + ")"
		}
		tunnelLabels = append(tunnelLabels, lbl)
	}
	tunnelIdx := 0
	sp := p.SSH
	if sp == nil {
		sp = &config.SSH{}
	} else {
		tunnelIdx = 1
		for i, al := range aliases {
			if al == sp.Alias {
				tunnelIdx = i + 2
			}
		}
	}
	tunnel := tview.NewDropDown().SetLabel("SSH tunnel").SetOptions(tunnelLabels, nil).SetCurrentOption(tunnelIdx)
	sshHost := tview.NewInputField().SetLabel("SSH host").SetText(sp.Host).SetFieldWidth(w)
	sshPort := tview.NewInputField().SetLabel("SSH port").SetFieldWidth(8).SetAcceptanceFunc(tview.InputFieldInteger).
		SetPlaceholder("22").SetPlaceholderTextColor(tcell.ColorGray)
	if sp.Port != 0 {
		sshPort.SetText(strconv.Itoa(sp.Port))
	}
	sshUser := tview.NewInputField().SetLabel("SSH user").SetText(sp.User).SetFieldWidth(w)
	sshKey := tview.NewInputField().SetLabel("SSH key file").SetText(sp.IdentityFile).SetFieldWidth(w).
		SetPlaceholder("empty: ssh-agent, ~/.ssh/id_*").SetPlaceholderTextColor(tcell.ColorGray)
	sshJump := tview.NewInputField().SetLabel("Jump hosts").SetText(sp.ProxyJump).SetFieldWidth(w).
		SetPlaceholder("e.g. bastion or user@host:port, …").SetPlaceholderTextColor(tcell.ColorGray)
	sshSecretChoices := []struct{ source, label string }{
		{"prompt", "ask when needed"},
		{"stored", "save after the first login"},
	}
	sshSecretIdx := 0
	if sp.Password == "stored" {
		sshSecretIdx = 1
	}
	sshSecret := tview.NewDropDown().SetLabel("SSH password").
		SetOptions([]string{sshSecretChoices[0].label, sshSecretChoices[1].label}, nil).SetCurrentOption(sshSecretIdx)
	sshFields := []tview.FormItem{sshHost, sshPort, sshUser, sshKey, sshJump, sshSecret}
	setTunnel := func(on bool) {
		for _, x := range sshFields {
			x.SetDisabled(!on)
		}
	}
	setTunnel(tunnelIdx > 0)

	for _, it := range []tview.FormItem{name, driver, host, port, socket, user, pw, pwSource, env, database,
		tunnel, sshHost, sshPort, sshUser, sshKey, sshJump, sshSecret,
		readOnly, autocommit, confirm, paramsField} {
		f.AddFormItem(it)
	}
	status := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	status.SetText("[gray]Tab/Shift+Tab move · Enter on a drop-down opens it · Esc cancels[-]")
	if sshCfgErr != nil {
		status.SetText("[yellow]" + tview.Escape(session.SSHConfigPath()+": "+sshCfgErr.Error()) + "[-]")
	}
	// Choosing a Host of ~/.ssh/config fills in its data (set after the
	// initial SetCurrentOption, so editing keeps the saved values).
	tunnel.SetSelectedFunc(func(_ string, i int) {
		setTunnel(i > 0)
		if i < 2 {
			if i == 1 {
				status.SetText("[gray]Host and Port above: the database as seen from the SSH server (often 127.0.0.1)[-]")
			}
			return
		}
		al := aliases[i-2]
		t, err := sshCfg.Resolve(al)
		if err != nil {
			status.SetText("[red::b]" + tview.Escape(err.Error()) + "[-::-]")
			return
		}
		sshHost.SetText(t.Host)
		sshPort.SetText("")
		if t.Port != 0 && t.Port != 22 {
			sshPort.SetText(strconv.Itoa(t.Port))
		}
		sshUser.SetText(t.User)
		sshKey.SetText("")
		if len(t.IdentityFiles) > 0 {
			sshKey.SetText(homeTilde(t.IdentityFiles[0]))
		}
		sshJump.SetText(sshCfg.Get(al, "proxyjump"))
		status.SetText("[gray]taken from ~/.ssh/config (" + tview.Escape(al) + "). Host and Port above: the database as seen from the SSH server (often 127.0.0.1)[-]")
	})

	// collect builds the profile and the password from the fields.
	collect := func() (*config.Profile, string, error) {
		np := &config.Profile{
			Name:          strings.TrimSpace(name.GetText()),
			Host:          strings.TrimSpace(host.GetText()),
			Socket:        strings.TrimSpace(socket.GetText()),
			User:          strings.TrimSpace(user.GetText()),
			Database:      strings.TrimSpace(database.GetText()),
			ReadOnly:      readOnly.IsChecked(),
			ConfirmWrites: confirm.IsChecked(),
		}
		di, _ := driver.GetCurrentOption()
		np.Driver = drivers[max(di, 0)].name
		if n, err := strconv.Atoi(strings.TrimSpace(port.GetText())); err == nil {
			np.Port = n
		}
		if !autocommit.IsChecked() {
			off := false
			np.Autocommit = &off
		}
		pi, _ := pwSource.GetCurrentOption()
		np.Password = pwChoices[max(pi, 0)].source
		if np.Password == "env:" {
			v := strings.TrimSpace(env.GetText())
			if v == "" {
				return nil, "", fmt.Errorf("enter the name of the environment variable")
			}
			np.Password += v
		}
		for _, kv := range strings.Split(paramsField.GetText(), ",") {
			kv = strings.TrimSpace(kv)
			if kv == "" {
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, "", fmt.Errorf("driver option %q: use key=value", kv)
			}
			if np.Params == nil {
				np.Params = map[string]string{}
			}
			np.Params[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		if ti, _ := tunnel.GetCurrentOption(); ti > 0 {
			si, _ := sshSecret.GetCurrentOption()
			np.SSH = &config.SSH{
				Host:         strings.TrimSpace(sshHost.GetText()),
				User:         strings.TrimSpace(sshUser.GetText()),
				IdentityFile: strings.TrimSpace(sshKey.GetText()),
				ProxyJump:    strings.TrimSpace(sshJump.GetText()),
				Password:     sshSecretChoices[max(si, 0)].source,
			}
			if ti >= 2 {
				np.SSH.Alias = aliases[ti-2]
			}
			if v := strings.TrimSpace(sshPort.GetText()); v != "" {
				np.SSH.Port, _ = strconv.Atoi(v)
			}
			// "user@host:port" typed into the host field
			if u, hh, pt, err := sshtunnel.SplitDest(np.SSH.Host); np.SSH.Host != "" && err == nil {
				np.SSH.Host = hh
				if u != "" && np.SSH.User == "" {
					np.SSH.User = u
				}
				if pt != 0 && np.SSH.Port == 0 {
					np.SSH.Port = pt
				}
			} else if np.SSH.Host != "" {
				return nil, "", err
			}
			if np.SSH.Alias == "" && np.SSH.Host == "" {
				return nil, "", fmt.Errorf("enter the SSH host (or choose a Host from ~/.ssh/config)")
			}
			if np.Socket != "" {
				return nil, "", fmt.Errorf("a socket cannot be reached through SSH: use host and port")
			}
		}
		if !config.ValidProfileName(np.Name) {
			return nil, "", fmt.Errorf("name: use letters, digits, '-', '_' or '.' (it becomes the path /%s)", np.Name)
		}
		if !editing || np.Name != orig.Name {
			if _, exists := a.cfg.Profiles[np.Name]; exists {
				return nil, "", fmt.Errorf("a connection named %q already exists", np.Name)
			}
		}
		if np.Host == "" && np.Socket == "" {
			return nil, "", fmt.Errorf("enter a host or a socket")
		}
		password := pw.GetText()
		if password == "" && hasStored && np.Password == "stored" {
			password, _, _ = config.StoredPassword(orig.Name)
		}
		return np, password, nil
	}
	showErr := func(err error) { status.SetText("[red::b]" + tview.Escape(err.Error()) + "[-::-]") }
	// A running test (SSH login, prompts) ends when the editor closes.
	testCtx, cancelTests := context.WithCancel(context.Background())
	close := func() { cancelTests(); a.closeModal("profile-edit", prev) }

	// credentials for test and save: typed password wins; otherwise the
	// configured source (prompt is skipped for the test).
	credsFor := func(np *config.Profile, password string) (config.Credentials, error) {
		if password != "" || np.Password == "stored" || np.Password == "prompt" {
			return config.Credentials{Host: np.Host, Port: np.Port, Socket: np.Socket, User: np.User, Password: password}, nil
		}
		return config.ResolveCredentials(np, nil)
	}
	// testedSig identifies the field values of the last successful test;
	// sshSecret is the SSH password or passphrase entered in it.
	var testedSig string
	var testedSSH struct{ kind, secret string } // SSH secret entered for the target in the last test
	// (by value: the profile holds pointers, which differ on every collect)
	signature := func(np *config.Profile, password string) string {
		v := *np
		v.SSH, v.Autocommit = nil, nil
		sig := fmt.Sprintf("%+v|%v|%s", v, np.IsAutocommit(), password)
		if np.SSH != nil {
			sig += fmt.Sprintf("|%+v", *np.SSH)
		}
		return sig
	}
	test := func(then func(ok bool)) {
		np, password, err := collect()
		if err != nil {
			showErr(err)
			return
		}
		status.SetText("[yellow]connecting…[-]")
		if np.SSH != nil {
			status.SetText("[yellow]connecting through SSH " + tview.Escape(np.SSH.Name()) + "…[-]")
		}
		sig := signature(np, password)
		go func() {
			cred, err := credsFor(np, password)
			var version, sshKind, sshSecret string
			if err == nil {
				// The test never stores anything: the SSH secret the
				// target asked for is kept for Save.
				hooks := session.Hooks{
					Prompt:         a.promptPassword,
					ConfirmHostKey: a.confirmHostKey,
					NoStore:        true,
					SecretUsed:     func(kind, secret string) { sshKind, sshSecret = kind, secret },
				}
				var s *session.Session
				if s, err = session.OpenWith(testCtx, np, cred, "", hooks); err == nil {
					version = s.Dialect.Name()
					s.Close()
				}
			}
			a.queue(func() {
				if err != nil {
					showErr(fmt.Errorf("connection failed: %w", err))
					if then != nil {
						then(false)
					}
					return
				}
				testedSig = sig
				testedSSH.kind, testedSSH.secret = sshKind, sshSecret
				msg := "[green::b]Connection works[-::-] (" + version + ")"
				if np.SSH != nil {
					msg += " through SSH " + tview.Escape(np.SSH.Name())
				}
				status.SetText(msg)
				if then != nil {
					then(true)
				}
			})
		}()
	}
	var save func(connect bool)
	save = func(connect bool) {
		np, password, err := collect()
		if err != nil {
			showErr(err)
			return
		}
		// An SSH tunnel is taken over only when the connection works
		// (owner request 2026-11-22): test first.
		if np.SSH != nil && signature(np, password) != testedSig {
			test(func(ok bool) {
				if ok {
					save(connect)
				}
			})
			return
		}
		var sshKind, sshSecretToStore string
		if np.SSH != nil && np.SSH.Password == "stored" {
			sshKind, sshSecretToStore = testedSSH.kind, testedSSH.secret
			if sshSecretToStore == "" && editing && sameSSHTarget(orig.SSH, np.SSH) {
				// keep the stored one (also on rename), but only for the
				// same SSH server, user and key
				sshKind, sshSecretToStore, _, _ = config.StoredSSHSecret(orig.Name)
			}
		}
		path := a.configPath()
		if editing && np.Name != orig.Name {
			if err := config.DeleteProfile(a.cfg, path, orig.Name); err != nil {
				showErr(err)
				return
			}
		}
		if err := config.SaveProfile(a.cfg, path, np); err != nil {
			showErr(err)
			return
		}
		switch {
		case np.Password == "stored" && password != "":
			if err := config.StorePassword(np.Name, password); err != nil {
				showErr(err)
				return
			}
		case np.Password != "stored":
			config.DeletePassword(np.Name) // the user chose not to keep it
		}
		if err := config.StoreSSHSecret(np.Name, sshKind, sshSecretToStore); err != nil { // "" removes it
			showErr(err)
			return
		}
		if s := a.ws.Sessions()[np.Name]; s != nil {
			a.log.Add(exec.Message{Info: "connection " + np.Name + " saved; changes apply after reconnecting (F2, x)"})
		} else {
			a.log.Add(exec.Message{Info: "connection " + np.Name + " saved to " + path})
		}
		close()
		if saved != nil {
			saved(np.Name)
		}
		if connect {
			a.closeModal("profiles", a.tree)
			a.invoke("cd", QuoteArg("/"+np.Name))
		}
	}
	f.AddButton("Test", func() { test(nil) })
	f.AddButton("Save", func() { save(false) })
	f.AddButton("Save & connect", func() { save(true) })
	f.AddButton("Cancel", close)
	f.SetCancelFunc(close)

	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(f, 0, 1, true).AddItem(status, 2, 0, false)
	_, _, _, h := a.pages.GetRect()
	a.modal("profile-edit", box, 76, min(34, max(h-2, 20)))
	a.tv.SetFocus(f)
}

// homeTilde shows a path below the home directory as ~/….
func homeTilde(p string) string {
	if home := config.HomeDir(); home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// sameSSHTarget reports whether two tunnels log in to the same server as
// the same user with the same key (a stored SSH secret stays valid).
func sameSSHTarget(a, b *config.SSH) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Alias == b.Alias && a.Host == b.Host && a.Port == b.Port && a.User == b.User &&
		a.IdentityFile == b.IdentityFile && a.ProxyJump == b.ProxyJump
}
