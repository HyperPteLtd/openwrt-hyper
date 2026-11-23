package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

// The LuCI pages read the router's codes and errors in Chinese: dom.js through diagnosticMessage
// (the log, task errors, the service's problem, the kernel page, an action's error), common.js
// for import notices and failed subscription operations, and records.js for the log's sources.
// A code without a text there reaches the page raw, and a text whose code nothing emits any more
// is dead weight.
//
// Every error the manager's own packages make can reach the page (an action's or a task's error,
// the service's problem), so each must read in Chinese unless unshown lists it: an errors.New,
// an error variable, a fmt.Errorf or errors.Join by a sample of its family, a context's errors
// and a file operation's. The other codes are read where they are emitted (emitters, and the
// notices, rejections and failures below), and followed through constants, assignments, tables,
// the cases of a switch on them, the checks on the way to them, the arguments callers pass and
// the literals a function returns. Calls are resolved by package, receiver type, interface,
// function value and struct field. What cannot be followed is listed in computed.

const luci = "../../files/www/luci-static/resources/hyper/"

// The dom.js maps diagnosticMessage reads.
var diagnosticMaps = []string{"diagnosticText", "errorText", "providerErrors", "kernelErrors"}

// The maps each kind of code is read from; common.js reads notices first. A key of a map must be
// emitted as a kind that reads it.
var readers = map[string][]string{
	"source":  {"sources"},
	"log":     diagnosticMaps,
	"notice":  append([]string{"notices"}, diagnosticMaps...),
	"failure": append([]string{"notices"}, diagnosticMaps...),
	"reason":  diagnosticMaps,
	"error":   diagnosticMaps,
}

type emitter struct {
	dir, fn string
	arg     int
	kind    string
}

// The calls whose argument is a code, by package and function (Type.method for a method).
// Within an emitter, a computed code is one it was given, checked where it is made.
var emitters = []emitter{
	{"internal/router", "Records.Log", 0, "source"}, {"internal/router", "Records.Log", 2, "log"},
	{"internal/router", "Records.LogDetail", 0, "source"}, {"internal/router", "Records.LogDetail", 2, "log"},
	{"internal/router", "Manager.phase", 0, "log"}, // "<phase> <error>", the error checked as all are
	{"internal/netguard", "dnsIssue", 1, "log"},
	{"internal/subscription", "Profile.notice", 0, "notice"},
	// "<action> <stage> <task>[ <code>]", checked by TestTaskNamesReadInChinese.
	{"cmd/hyperd", "app.runJob", 1, "action"},
}

// The manager's packages, whose errors reach the page.
var shownPackages = []string{"cmd/hyperd", "internal/router", "internal/control"}

// The subscription helper reports these packages' errors as codes (subscriptioncli.code): a
// snake_case error as it is, an English one with underscores for its spaces.
var failurePackages = []string{"internal/subscription", "internal/cli/subscriptioncli", "internal/proxyspec", "internal/hypersync"}

// Errors that never reach the page.
var unshown = []string{
	"record unchanged",                // errUnchanged: an update leaving the record as it is
	"hot reload fell back to restart", // errHotFallback: the service restarts the core instead
	"change needs a full restart",     // ErrNeedsRestart: likewise, and not logged
	"manager rejected request",        // errRejected: the command line's call
	"manager already running",         // serve, on the command line
	"network could not be restored",   // uninstall, on the command line
	"expected serve, guard, uninstall, purge, notify-clock, notify-network, notify-source SOURCE or call METHOD",
}

// The codes and task steps of the imports this version removed: subscriptions by link, by file
// and by another panel's account, with their conversion, templates, providers and request
// diagnostics. Nothing emits them any more; the pages may keep their texts until they drop them.
var retired = func() map[string]bool {
	out := map[string]bool{}
	for _, code := range strings.Fields(`
		authenticated_redirect_refused entitlement_check_failed expanded_subscription_limit
		external_source_required invalid_external_source invalid_file_source invalid_login
		invalid_login_response invalid_login_token invalid_node_inventory invalid_origin
		invalid_plan_detail invalid_plan_list invalid_platform_site invalid_platform_source
		invalid_provider_choice invalid_request invalid_response_size invalid_template_filter
		not_external platform_account_revoked redirect_refused rule_file_size_exceeds_limit
		source_config_required source_preferences_required subscription_app_disabled
		subscription_empty subscription_incomplete subscription_link_unavailable
		subscription_link_unreachable subscription_message subscription_redirect_refused
		subscription_request_failed subscription_tls_untrusted subscription_web_page
		template_cache_write_failed template_rule_set_unavailable verified_login_required
		invalid_credential mihomo_router_field_ignored node_missing_credential
		nonstandard_snell_credential script unavailable_inventory
		provider_download_failed provider_option_ignored provider_option_unsafe
		invalid_provider_url invalid_proxy_provider provider_cancelled provider_connection_refused
		provider_dns_failed provider_incomplete provider_limit provider_redirect_refused
		provider_timeout provider_tls_untrusted
		configure preferences site`) {
		out[code] = true
	}
	return out
}()

// The warnings of the converter the removed imports used, which the sources page read as
// notices named after the converter: "<converter>_<warning>".
var converterWarnings = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`cyclic_reference empty_group ignored_option invalid_proxy local_provider renamed
		template_unavailable unknown_reference unsupported_group unsupported_option unsupported_provider
		unsupported_proxy unsupported_rule unsupported_rule_set`) {
		converterWarnings[w] = true
	}
}

// retiredText reports a text of the pages for a code of the removed imports.
func retiredText(name, key string) bool {
	_, warning, _ := strings.Cut(key, "_")
	return retired[key] || name == "notices" && converterWarnings[warning]
}

// Emission points whose value is computed at run time, by package directory and function: the
// codes they can emit (each a string literal of the source), or samples of the families they
// emit, which a pattern of diagnosticMessage reads.
var computed = []struct {
	at, kind string
	codes    []string
	samples  []string
}{
	// A DNS issue the network helper stopped at, or its prerequisites check found: "<code>
	// <dnsmasq instance>".
	{"internal/router:command", "error", nil, []string{"dns_shared_instance lan"}},
	{"internal/router:checkNetworkRequirements", "error", nil, []string{"dns_no_instance lan"}},
	// A node dropped for a broken dialer-proxy chain, or for its own error (addProxy, proxyspec).
	{"internal/subscription:Profile.chains", "notice", []string{"dialer_proxy_unavailable", "dialer_proxy_cycle"}, nil},
	{"internal/subscription:parseClash", "notice", []string{"invalid_endpoint", "node_limit", "unsupported_node", "mihomo_protocol_unsupported"}, nil},
	// A failed refresh keeps the error's code (collected where it is made), or update_failed.
	{"internal/subscription:Fetcher.Refresh", "failure", []string{"update_failed"}, nil},
	// The certificate check a kernel download failed, as the kernel helper reports it.
	{"cmd/hyperd:publicKernelError", "error", nil, []string{
		(&tlsdiag.Diagnostic{Reason: "untrusted"}).Wire(),
		(&tlsdiag.Diagnostic{Reason: "expired", CheckedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}).Wire(),
	}},
}

// Families read alike wherever they are made: the kernel's rejections, "<object>: <reason>" with
// a generated node or group ID, an entry of the configuration or the whole configuration
// (kernelcli rejection, router Issue), and a failed file operation with its path.
var familySamples = func() []string {
	out := []string{
		"node:0123456789abcdef0123456789abcdef: candidate core rejected vmess configuration",
		"group:0123456789abcdef0123456789abcdef: no compatible members on this device",
		"proxy group[2]: candidate core rejected the configuration",
		"configuration: candidate core rejected the configuration",
	}
	for _, errno := range []syscall.Errno{syscall.EROFS, syscall.ENOSPC, syscall.EACCES, syscall.EPERM, syscall.EIO, syscall.ENOENT} {
		out = append(out, (&os.LinkError{Op: "rename", Old: "/a", New: "/b", Err: errno}).Error(),
			(&os.PathError{Op: "open", Path: "/a b", Err: errno}).Error())
	}
	return out
}()

func TestEmittedCodesReadInChinese(t *testing.T) {
	src := parseRouter(t)
	found := src.emitted(t)
	l := readLuci(t)
	var problems []string
	for kind, set := range found.codes {
		for code, at := range set {
			if readers[kind] != nil && !l.readable(kind, code) {
				problems = append(problems, fmt.Sprintf("%s %q (%s) has no Chinese text in %v", kind, code, at, readers[kind]))
			}
		}
	}
	for _, x := range found.samples {
		if !l.readable(x.kind, x.text) {
			problems = append(problems, fmt.Sprintf("%s %q (%s) has no Chinese text: add a dom.js errorPatterns entry for its family", x.kind, x.text, x.at))
		}
	}
	for _, x := range computed {
		for _, sample := range x.samples {
			if !l.family(sample) {
				problems = append(problems, fmt.Sprintf("computed sample %q (%s) is read by no pattern of diagnosticMessage", sample, x.at))
			}
		}
	}
	for _, sample := range familySamples {
		if !l.family(sample) {
			problems = append(problems, fmt.Sprintf("%q has no dom.js errorPatterns entry", sample))
		}
	}

	english := src.englishErrors(t)
	for name, keys := range l.maps {
		var kinds []string
		for kind, maps := range readers {
			if slices.Contains(maps, name) {
				kinds = append(kinds, kind)
			}
		}
		for key := range keys {
			if len(kinds) == 0 || retiredText(name, key) || slices.ContainsFunc(kinds, func(kind string) bool { _, ok := found.codes[kind][key]; return ok }) {
				continue
			}
			// An English error of the kernel helper or a shell helper, which reach the page in
			// their own words.
			if (name == "diagnosticText" || name == "errorText" || name == "kernelErrors") && !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(key) && english(key) {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s has a text for %q, which nothing emits", name, key))
		}
	}
	report(t, problems)
}

// runJob logs "<action> <stage> <task>[ <code>]" when a job ends; taskName reads the action, a
// head and step for one taskText does not name in full.
func TestTaskNamesReadInChinese(t *testing.T) {
	src := parseRouter(t)
	codes := src.emitted(t).codes
	l := readLuci(t)
	maps := l.maps
	var problems []string
	heads, steps := map[string]bool{}, map[string]bool{}
	for action, at := range codes["action"] {
		if maps["taskText"][action] {
			heads[action] = true
			continue
		}
		head, step, _ := strings.Cut(action, "/")
		heads[head] = true
		if step != "" {
			steps[step] = true
		}
		if !maps["taskText"][head] || step != "" && !maps["taskStep"][step] && !maps["diagnosticText"][step] {
			problems = append(problems, fmt.Sprintf("task %q (%s) has no Chinese name in taskText and taskStep", action, at))
		}
	}
	for key := range maps["taskText"] {
		if !heads[key] {
			problems = append(problems, fmt.Sprintf("taskText names %q, which no job runs", key))
		}
	}
	for key := range maps["taskStep"] {
		if !steps[key] && !retired[key] {
			problems = append(problems, fmt.Sprintf("taskStep names %q, which no job runs", key))
		}
	}

	// The stages a finished job is logged with, and held for a refresh whose update waits for
	// the administrator (heldUpdate), which the log line follows with the pending update's code.
	stages := map[string]bool{}
	runJob := src.pkgs["cmd/hyperd"].funcs["app.runJob"]
	if runJob == nil {
		t.Fatal("runJob not found")
	}
	ast.Inspect(runJob.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if sel, ok := n.Lhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "Stage" {
				if lit, ok := n.Rhs[0].(*ast.BasicLit); ok {
					v, _ := strconv.Unquote(lit.Value)
					stages[v] = true
				}
			}
		case *ast.BasicLit:
			if m := regexp.MustCompile(`^" ([a-z]+) "$`).FindStringSubmatch(n.Value); m != nil {
				stages[m[1]] = true
			}
		}
		return true
	})
	if !maps["taskStage"]["held"] || !stages["held"] {
		problems = append(problems, fmt.Sprintf("runJob logs a held refresh as %v; taskStage reads %v", stages, maps["taskStage"]))
	}
	if len(stages) != len(maps["taskStage"]) {
		problems = append(problems, fmt.Sprintf("runJob logs the stages %v; taskStage reads %v", stages, maps["taskStage"]))
	}
	read := regexp.MustCompile(`const task = /\^\(\\S\+\) \(([a-z|]+)\) `).FindStringSubmatch(l.dom)
	if read == nil {
		t.Fatal("diagnosticMessage's task line not found")
	}
	for stage := range stages {
		if !maps["taskStage"][stage] {
			problems = append(problems, fmt.Sprintf("stage %q has no Chinese text in taskStage", stage))
		}
		if !slices.Contains(strings.Split(read[1], "|"), stage) {
			problems = append(problems, fmt.Sprintf("diagnosticMessage does not read the stage %q", stage))
		}
	}
	pending := []string{subscription.PendingConfirmation, subscription.PendingBlocked}
	for _, code := range pending {
		if !maps["diagnosticText"][code] || !maps["pendingUpdates"][code] {
			problems = append(problems, fmt.Sprintf("held update %q needs a text in diagnosticText (the log) and pendingUpdates (its card)", code))
		}
	}
	if len(maps["pendingUpdates"]) != len(pending) {
		problems = append(problems, fmt.Sprintf("pendingUpdates reads %v; the router holds updates as %v", maps["pendingUpdates"], pending))
	}
	report(t, problems)
}

// report lists the problems found in order, so that each run reads alike.
func report(t *testing.T, problems []string) {
	t.Helper()
	sort.Strings(problems)
	for _, p := range slices.Compact(problems) {
		t.Error(p)
	}
}

// The source

type routerSource struct {
	fset     *token.FileSet
	pkgs     map[string]*pkg // by package directory, relative to the module
	dirs     []string        // sorted
	literals map[string]bool // every string literal
	targets  map[*ast.CallExpr][]target
	callers  map[ast.Node][]callSite // by function declaration or literal, built on first use
	fields   map[string][]target     // the functions a struct field is given, by Type.field
	args     map[argKey]result
}

type pkg struct {
	files []*ast.File
	funcs map[string]*ast.FuncDecl // functions, and methods as Type.name
	types map[string]*ast.TypeSpec
	vars  map[string]*ast.ValueSpec // package-level constants and variables
}

// scope is where an expression sits: its package directory, file and enclosing function, and
// what the checks on the way to it tell of its names (env).
type scope struct {
	dir  string
	file *ast.File
	fn   *ast.FuncDecl
	env  map[string]constraint
}

// constraint is what a name can hold: one of in (nil: anything), never one of out.
type constraint struct{ in, out []string }

// target is a function a call can reach: a declaration, or a function literal in one.
type target struct {
	c   scope
	lit *ast.FuncLit
}

type callSite struct {
	c    scope
	call *ast.CallExpr
}

type argKey struct {
	fn ast.Node
	i  int
}

type result struct {
	values []string
	ok     bool
}

func (t target) node() ast.Node {
	if t.lit != nil {
		return t.lit
	}
	return t.c.fn
}

func (t target) body() *ast.BlockStmt {
	if t.lit != nil {
		return t.lit.Body
	}
	return t.c.fn.Body
}

// parseRouter reads the Go programs that run on the router.
func parseRouter(t *testing.T) *routerSource {
	t.Helper()
	s := &routerSource{fset: token.NewFileSet(), pkgs: map[string]*pkg{}, literals: map[string]bool{},
		targets: map[*ast.CallExpr][]target{}, fields: map[string][]target{}, args: map[argKey]result{}}
	for _, root := range []string{"cmd/hyperd", "cmd/hyper-kernel", "cmd/hyper-net", "cmd/hyper-subscription", "internal"} {
		err := filepath.WalkDir("../../"+root, func(p string, d fs.DirEntry, err error) error {
			rel := strings.TrimPrefix(filepath.ToSlash(p), "../../")
			if err != nil || rel == "internal/release" {
				if err == nil {
					err = filepath.SkipDir // release tooling, not on the router
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(s.fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			dir := filepath.ToSlash(filepath.Dir(rel))
			if s.pkgs[dir] == nil {
				s.pkgs[dir] = &pkg{funcs: map[string]*ast.FuncDecl{}, types: map[string]*ast.TypeSpec{}, vars: map[string]*ast.ValueSpec{}}
				s.dirs = append(s.dirs, dir)
			}
			p2 := s.pkgs[dir]
			p2.files = append(p2.files, f)
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok {
					p2.funcs[funcName(fn)] = fn
					continue
				}
				for _, spec := range d.(*ast.GenDecl).Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						p2.types[spec.Name.Name] = spec
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							p2.vars[name.Name] = spec
						}
					}
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					s.literals[v] = true
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(s.dirs)
	return s
}

// funcName names a function, or a method as Type.name.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return baseType(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func baseType(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return baseType(e.X)
	case *ast.IndexExpr:
		return baseType(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func (s *routerSource) fileOf(dir string, n ast.Node) *ast.File {
	for _, f := range s.pkgs[dir].files {
		if f.Pos() <= n.Pos() && n.End() <= f.End() {
			return f
		}
	}
	return nil
}

func (s *routerSource) declScope(dir string, fn *ast.FuncDecl) scope {
	return scope{dir: dir, file: s.fileOf(dir, fn), fn: fn}
}

// site names where an expression sits: its package and function.
func (s *routerSource) site(c scope) string {
	if c.fn == nil {
		return c.dir + ":"
	}
	return c.dir + ":" + funcName(c.fn)
}

// each visits every declaration of the module in a fixed order.
func (s *routerSource) each(visit func(c scope, n ast.Node) bool) {
	for _, dir := range s.dirs {
		for _, f := range s.pkgs[dir].files {
			for _, d := range f.Decls {
				c := scope{dir: dir, file: f}
				if fn, ok := d.(*ast.FuncDecl); ok {
					c.fn = fn
				}
				ast.Inspect(d, func(n ast.Node) bool { return visit(c, n) })
			}
		}
	}
}

// What is emitted

type sample struct{ kind, text, at string }

type found struct {
	codes   map[string]map[string]string // by kind, each with where it was found
	samples []sample                     // of a family of codes, which a pattern must read
}

type walk struct {
	*routerSource
	found
	unresolved []string
	used       map[int]bool // computed entries that were needed
}

// emitted returns the codes of every emission point by kind, each with where it was found.
func (s *routerSource) emitted(t *testing.T) found {
	t.Helper()
	w := &walk{routerSource: s, found: found{codes: map[string]map[string]string{}}, used: map[int]bool{}}
	sentinels := s.sentinels(t)
	errorCode := regexp.MustCompile(`^[a-z][a-z0-9]*([_ ][a-z0-9%]+)+$`)
	notValue := map[ast.Expr]bool{}
	s.each(func(c scope, n ast.Node) bool {
		notValues(n, notValue)
		shown := slices.Contains(shownPackages, c.dir)
		switch n := n.(type) {
		case *ast.CallExpr:
			for _, t := range s.call(c, n) {
				for _, e := range emitters {
					if e.dir == t.c.dir && t.lit == nil && e.fn == funcName(t.c.fn) && e.arg < len(n.Args) {
						w.emit(e.kind, c, n.Args[e.arg])
					}
				}
			}
			pkg, name := s.stdCall(c, n)
			switch {
			case shown && pkg == "errors" && name == "New":
				w.emit("error", c, n.Args[0])
			case shown && (pkg == "fmt" && name == "Errorf" || pkg == "errors" && name == "Join"):
				w.samples = append(w.samples, sample{"error", w.format(c, n), s.site(c)})
			case shown && pkg == "" && name == "Err" && len(n.Args) == 0:
				w.add("error", context.DeadlineExceeded.Error(), s.site(c))
				w.add("error", context.Canceled.Error(), s.site(c))
			case slices.Contains(failurePackages, c.dir) && (pkg == "errors" && name == "New" || pkg == "fmt" && name == "Errorf"):
				lit, ok := n.Args[0].(*ast.BasicLit)
				if !ok {
					break
				}
				v, _ := strconv.Unquote(lit.Value)
				if !errorCode.MatchString(v) || sentinels[v] || slices.Contains(unshown, v) {
					break
				}
				if strings.Contains(v, "%") {
					w.samples = append(w.samples, sample{"failure", strings.ReplaceAll(w.format(c, n), " ", "_"), s.site(c)})
				} else {
					w.add("failure", strings.ReplaceAll(v, " ", "_"), s.site(c))
				}
			}
		case *ast.SelectorExpr:
			// An error variable of another package, which the manager passes on.
			if v, ok := s.errorVar(c, n); ok && shown {
				w.add("error", v, s.site(c))
			}
		case *ast.CompositeLit:
			if list, ok := n.Type.(*ast.ArrayType); ok && baseType(list.Elt) == "Notice" {
				for _, e := range n.Elts {
					if row, ok := e.(*ast.CompositeLit); ok && row.Type == nil {
						if v := field(row, "Code", -1); v != nil {
							w.emit("notice", c, v)
						}
					}
				}
			}
			switch baseType(n.Type) {
			case "Notice":
				if v := field(n, "Code", -1); v != nil {
					w.emit("notice", c, v)
				}
			case "Issue", "configRejection":
				if v := field(n, "Reason", 2); v != nil {
					w.emit("reason", c, v)
				}
			}
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				sel, ok := l.(*ast.SelectorExpr)
				if !ok || len(n.Rhs) != len(n.Lhs) {
					continue
				}
				switch {
				case sel.Sel.Name == "Reason" && c.dir == "internal/cli/kernelcli":
					w.emit("reason", c, n.Rhs[i]) // a rejection's reason set after it was made
				case sel.Sel.Name == "LastFailure":
					w.emit("failure", c, n.Rhs[i]) // what a source's card shows
				case sel.Sel.Name == "Code" && s.typeOf(c, sel.X, 0).name == "Notice":
					w.emit("notice", c, n.Rhs[i]) // a notice's code changed after it was made
				}
			}
		}
		return true
	})
	// An emitter passed on as a function value has calls the guard cannot see.
	s.each(func(c scope, n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && !notValue[e] && !s.alias(c, e) {
			for _, t := range s.resolve(c, e, false) {
				if s.isEmitter(t) {
					w.unresolved = append(w.unresolved, fmt.Sprintf("%s: %s is passed on as a value in %s; call it, or name it in a variable of the function that calls it", s.fset.Position(e.Pos()), funcName(t.c.fn), s.site(c)))
				}
			}
		}
		return true
	})

	// The codes subscriptioncli.code gives the errors it classifies, and a deadline's error,
	// which it passes on as context_deadline_exceeded.
	ast.Inspect(s.pkgs["internal/cli/subscriptioncli"].funcs["code"], func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
			if lit, ok := r.Results[0].(*ast.BasicLit); ok {
				v, _ := strconv.Unquote(lit.Value)
				w.add("failure", v, "internal/cli/subscriptioncli:code")
			}
		}
		return true
	})
	w.add("failure", strings.ReplaceAll(context.DeadlineExceeded.Error(), " ", "_"), "internal/cli/subscriptioncli:code")
	// runJob logs a held refresh with its pending update's code (heldUpdate).
	for _, code := range []string{subscription.PendingConfirmation, subscription.PendingBlocked} {
		w.add("log", code, "cmd/hyperd:heldUpdate")
	}
	// A task the manager's restart interrupted, which the kernel page shows for the bootstrap job.
	interrupted := false
	ast.Inspect(s.pkgs["cmd/hyperd"].funcs["app.loadJobs"], func(n ast.Node) bool {
		if a, ok := n.(*ast.AssignStmt); ok && len(a.Lhs) == 1 && len(a.Rhs) == 1 {
			sel, isField := a.Lhs[0].(*ast.SelectorExpr)
			lit, isLit := a.Rhs[0].(*ast.BasicLit)
			if isField && isLit && sel.Sel.Name == "Error" {
				v, _ := strconv.Unquote(lit.Value)
				w.add("error", v, "cmd/hyperd:app.loadJobs")
				interrupted = true
			}
		}
		return true
	})
	if !interrupted {
		t.Fatal("loadJobs's error for an interrupted task not found")
	}
	for i, x := range computed {
		if !w.used[i] {
			w.unresolved = append(w.unresolved, fmt.Sprintf("computed lists %s %s, which emits nothing computed any more", x.at, x.kind))
		}
		for _, code := range x.codes {
			if !s.literals[code] {
				w.unresolved = append(w.unresolved, fmt.Sprintf("computed lists the code %q for %s, which is no string literal of the source; list it as a sample", code, x.at))
			}
		}
	}
	// What cannot be followed would read as texts nothing emits: stop before that.
	if len(w.unresolved) > 0 {
		report(t, w.unresolved)
		t.FailNow()
	}
	return w.found
}

func (w *walk) add(kind, code, at string) {
	if code == "" || slices.Contains(unshown, code) {
		return
	}
	if w.codes[kind] == nil {
		w.codes[kind] = map[string]string{}
	}
	if _, ok := w.codes[kind][code]; !ok {
		w.codes[kind][code] = at
	}
}

// emit records the codes an expression can hold: its values, a sample of a text made of
// literals and computed parts, or what computed lists.
func (w *walk) emit(kind string, c scope, e ast.Expr) {
	values, ok := w.values(c, e, 0)
	if !ok && c.fn != nil {
		values, ok = w.alongPaths(c, e)
	}
	switch text, sampled := w.concatenation(c, e, 0); {
	case ok:
		for _, v := range values {
			w.add(kind, v, w.site(c))
		}
	case c.fn != nil && w.isEmitter(target{c: c}):
		// The caller's code, checked where it is called.
	case sampled:
		w.samples = append(w.samples, sample{kind, text, w.site(c)})
	default:
		for i, x := range computed {
			if x.at == w.site(c) && x.kind == kind {
				w.used[i] = true
				for _, v := range x.codes {
					w.add(kind, v, x.at)
				}
				return
			}
		}
		w.unresolved = append(w.unresolved, fmt.Sprintf("%s: computed %s code in %s; follow it to literals or list it in computed", w.fset.Position(e.Pos()), kind, w.site(c)))
	}
}

// concatenation returns a sample of a string made of literals and computed parts, with x for
// each computed part, when it has a literal part.
func (w *walk) concatenation(c scope, e ast.Expr, depth int) (string, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.BinaryExpr:
		l, lok := w.concatenation(c, e.X, depth+1)
		r, rok := w.concatenation(c, e.Y, depth+1)
		return l + r, e.Op == token.ADD && (lok || rok)
	case *ast.BasicLit:
		v, err := strconv.Unquote(e.Value)
		return v, err == nil && depth > 0
	case *ast.Ident:
		for _, a := range w.assignments(c, e.Name) {
			if _, ok := a.rhs.(*ast.BinaryExpr); ok && a.tok == token.DEFINE && depth < 4 {
				return w.concatenation(c, a.rhs, depth+1)
			}
		}
	}
	return "x", false
}

// format returns a sample of what fmt.Errorf or errors.Join make: the format, with an error each
// wrapped error can be, 123 for a number and x for the rest.
func (w *walk) format(c scope, call *ast.CallExpr) string {
	if _, name := w.stdCall(c, call); name == "Join" {
		var parts []string
		for _, a := range call.Args {
			parts = append(parts, w.oneError(c, a, 0))
		}
		return strings.Join(parts, "\n")
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return "x"
	}
	format, _ := strconv.Unquote(lit.Value)
	i := 0
	return regexp.MustCompile(`%[a-z]`).ReplaceAllStringFunc(format, func(verb string) string {
		if i++; verb == "%d" {
			return "123"
		} else if i < len(call.Args) {
			return w.oneError(c, call.Args[i], 0)
		}
		return "x"
	})
}

// oneError returns a text an error can have: a context's, an errors.New's or an error
// variable's, or x.
func (w *walk) oneError(c scope, e ast.Expr, depth int) string {
	switch e := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		switch pkg, name := w.stdCall(c, e); {
		case pkg == "" && name == "Err" && len(e.Args) == 0:
			return context.DeadlineExceeded.Error()
		case pkg == "errors" && name == "New":
			if v, ok := w.values(c, e.Args[0], 0); ok && len(v) > 0 {
				return v[0]
			}
		}
	case *ast.Ident:
		if v, ok := w.errorVar(c, e); ok {
			return v
		}
		for _, a := range w.assignments(c, e.Name) {
			if depth > 3 || a.rhs == nil {
				break
			}
			if v := w.oneError(c, a.rhs, depth+1); v != "x" {
				return v
			}
		}
	}
	return "x"
}

// errorVar returns the message of an error variable made with errors.New, of this module.
func (s *routerSource) errorVar(c scope, e ast.Expr) (string, bool) {
	dir, spec, ok := s.packageVar(c, e)
	if !ok || len(spec.Values) == 0 {
		return "", false
	}
	call, ok := spec.Values[0].(*ast.CallExpr)
	vc := scope{dir: dir, file: s.fileOf(dir, spec)}
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	if pkg, name := s.stdCall(vc, call); pkg != "errors" || name != "New" {
		return "", false
	}
	v, ok := s.values(vc, call.Args[0], 0)
	if !ok || len(v) != 1 {
		return "", false
	}
	return v[0], true
}

// packageVar resolves a name of this package, or of a package of this module, to its declaration.
func (s *routerSource) packageVar(c scope, e ast.Expr) (string, *ast.ValueSpec, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		if c.fn != nil && s.local(c, e.Name) {
			return "", nil, false
		}
		spec, ok := s.pkgs[c.dir].vars[e.Name]
		return c.dir, spec, ok
	case *ast.SelectorExpr:
		if dir, _, ok := s.imported(c, e.X); ok && s.pkgs[dir] != nil {
			spec, ok := s.pkgs[dir].vars[e.Sel.Name]
			return dir, spec, ok
		}
	}
	return "", nil, false
}

// stdCall names a call to a package outside the module by its import path, to a package of the
// module by its directory, or a method or local function ("", name).
func (s *routerSource) stdCall(c scope, call *ast.CallExpr) (pkg, name string) {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.SelectorExpr:
		if dir, path, ok := s.imported(c, f.X); ok && s.pkgs[dir] != nil {
			return dir, f.Sel.Name
		} else if ok {
			return path, f.Sel.Name
		}
		return "", f.Sel.Name
	case *ast.Ident:
		return "", f.Name
	}
	return "", ""
}

func (s *routerSource) isEmitter(t target) bool {
	return t.lit == nil && slices.ContainsFunc(emitters, func(e emitter) bool { return e.dir == t.c.dir && e.fn == funcName(t.c.fn) })
}

// notValues records the expressions of a node that are no function value passed on: the function
// a call calls, a variable assigned, a selector's field or method, a literal's key and a
// declaration's name.
func notValues(n ast.Node, skip map[ast.Expr]bool) {
	switch n := n.(type) {
	case *ast.CallExpr:
		skip[ast.Unparen(n.Fun)] = true
	case *ast.AssignStmt:
		for _, l := range n.Lhs {
			skip[l] = true
		}
	case *ast.SelectorExpr:
		skip[n.Sel] = true
	case *ast.KeyValueExpr:
		skip[n.Key] = true
	case *ast.FuncDecl:
		skip[n.Name] = true
	}
}

// alias reports whether e is a function value a local variable of its function holds.
func (s *routerSource) alias(c scope, e ast.Expr) bool {
	isAlias := false
	if c.fn != nil {
		ast.Inspect(c.fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, r := range n.Rhs {
					isAlias = isAlias || len(n.Lhs) == len(n.Rhs) && isIdent(n.Lhs[i]) && exprKey(n.Lhs[i]) != "_" && r == e
				}
			case *ast.ValueSpec:
				isAlias = isAlias || slices.Contains(n.Values, e)
			}
			return !isAlias
		})
	}
	return isAlias
}

// Calls

// call returns the functions a call can reach.
func (s *routerSource) call(c scope, call *ast.CallExpr) []target {
	if t, ok := s.targets[call]; ok {
		return t
	}
	s.targets[call] = nil // a cycle reaches nothing more
	t := s.resolve(c, call.Fun, true)
	s.targets[call] = t
	return t
}

// resolve returns the functions an expression names: a function of the package or an imported
// one, a method by the type of its receiver (every implementation of an interface; for a call,
// every method of that name in the package when the type is unknown), the function values a
// local variable or a struct field holds, or a function literal.
func (s *routerSource) resolve(c scope, fun ast.Expr, called bool) []target {
	switch f := ast.Unparen(fun).(type) {
	case *ast.FuncLit:
		return []target{{c: c, lit: f}}
	case *ast.Ident:
		if c.fn != nil && s.local(c, f.Name) {
			var out []target
			for _, a := range s.assignments(c, f.Name) {
				switch rhs := a.rhs.(type) {
				case *ast.FuncLit, *ast.SelectorExpr:
					out = append(out, s.resolve(c, rhs, false)...)
				}
			}
			return out
		}
		if fn := s.pkgs[c.dir].funcs[f.Name]; fn != nil {
			return []target{{c: s.declScope(c.dir, fn)}}
		}
	case *ast.SelectorExpr:
		if dir, _, ok := s.imported(c, f.X); ok {
			if p := s.pkgs[dir]; p != nil && p.funcs[f.Sel.Name] != nil {
				return []target{{c: s.declScope(dir, p.funcs[f.Sel.Name])}}
			}
			return nil
		}
		switch t := s.typeOf(c, f.X, 0); {
		case t.dir != "":
			return s.member(t, f.Sel.Name, 0)
		case t.name == "" && called:
			return s.methodsNamed(c.dir, f.Sel.Name)
		}
	}
	return nil
}

// member returns the functions T.name can be: its method, an embedded type's, every method of
// that name in the package for an interface, or the functions a field of that name is given.
func (s *routerSource) member(t typeRef, name string, depth int) []target {
	p := s.pkgs[t.dir]
	if fn := p.funcs[t.name+"."+name]; fn != nil {
		return []target{{c: s.declScope(t.dir, fn)}}
	}
	spec := p.types[t.name]
	if spec == nil || depth > 3 {
		return nil
	}
	switch st := spec.Type.(type) {
	case *ast.InterfaceType:
		return s.methodsNamed(t.dir, name)
	case *ast.StructType:
		for _, f := range st.Fields.List {
			if _, isFunc := f.Type.(*ast.FuncType); isFunc && paramField(&ast.FieldList{List: []*ast.Field{f}}, name) != nil {
				return s.fieldFuncs(t, name)
			}
			if e := s.typeExpr(scope{dir: t.dir, file: s.fileOf(t.dir, spec)}, f.Type); len(f.Names) == 0 && e.dir != "" {
				if out := s.member(e, name, depth+1); out != nil {
					return out
				}
			}
		}
	}
	return nil
}

func (s *routerSource) methodsNamed(dir, name string) []target {
	var keys []string
	for k := range s.pkgs[dir].funcs {
		if strings.HasSuffix(k, "."+name) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []target
	for _, k := range keys {
		out = append(out, target{c: s.declScope(dir, s.pkgs[dir].funcs[k])})
	}
	return out
}

// fieldFuncs returns the functions the package gives a struct's field, in a literal of the
// struct or assigned to the field.
func (s *routerSource) fieldFuncs(t typeRef, name string) []target {
	k := t.dir + ":" + t.name + "." + name
	if out, ok := s.fields[k]; ok {
		return out
	}
	s.fields[k] = nil // a cycle adds nothing
	var out []target
	for _, f := range s.pkgs[t.dir].files {
		for _, d := range f.Decls {
			c := scope{dir: t.dir, file: f}
			c.fn, _ = d.(*ast.FuncDecl)
			ast.Inspect(d, func(n ast.Node) bool {
				var v ast.Expr
				switch n := n.(type) {
				case *ast.CompositeLit:
					if baseType(n.Type) == t.name {
						v = field(n, name, -1)
					}
				case *ast.AssignStmt:
					if sel, ok := n.Lhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == name && len(n.Rhs) == 1 {
						v = n.Rhs[0]
					}
				}
				if v != nil {
					out = append(out, s.resolve(c, v, false)...)
				}
				return true
			})
		}
	}
	s.fields[k] = out
	return out
}

// callersOf returns the calls of the module that reach a function.
func (s *routerSource) callersOf(t target) []callSite {
	if s.callers == nil {
		s.callers = map[ast.Node][]callSite{}
		s.each(func(c scope, n ast.Node) bool {
			if n, ok := n.(*ast.CallExpr); ok {
				for _, t := range s.call(c, n) {
					s.callers[t.node()] = append(s.callers[t.node()], callSite{c, n})
				}
			}
			return true
		})
	}
	return s.callers[t.node()]
}

// Types

// typeRef is a named type: its package directory, "" for one outside the module, whose name
// then carries its import path.
type typeRef struct{ dir, name string }

// typeOf returns the type of an expression, as far as its declarations tell.
func (s *routerSource) typeOf(c scope, e ast.Expr, depth int) typeRef {
	if depth > 6 {
		return typeRef{}
	}
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		if c.fn != nil && s.local(c, e.Name) {
			if f := paramField(c.fn.Recv, e.Name); f != nil {
				return s.typeExpr(c, f.Type)
			}
			if f := paramField(c.fn.Type.Params, e.Name); f != nil {
				return s.typeExpr(c, f.Type)
			}
			for _, a := range s.assignments(c, e.Name) {
				if a.typ != nil {
					return s.typeExpr(c, a.typ)
				} else if t := s.typeOf(c, a.rhs, depth+1); a.index == 0 && t.name != "" {
					return t
				}
			}
		} else if spec := s.pkgs[c.dir].vars[e.Name]; spec != nil {
			vc := scope{dir: c.dir, file: s.fileOf(c.dir, spec)}
			if spec.Type != nil {
				return s.typeExpr(vc, spec.Type)
			}
			return s.typeOf(vc, spec.Values[0], depth+1)
		}
	case *ast.SelectorExpr:
		x := s.typeOf(c, e.X, depth+1)
		if _, _, imported := s.imported(c, e.X); x.dir == "" || imported || s.pkgs[x.dir].types[x.name] == nil {
			return typeRef{}
		}
		spec := s.pkgs[x.dir].types[x.name]
		if st, ok := spec.Type.(*ast.StructType); ok {
			for _, f := range st.Fields.List {
				if paramField(&ast.FieldList{List: []*ast.Field{f}}, e.Sel.Name) != nil {
					return s.typeExpr(scope{dir: x.dir, file: s.fileOf(x.dir, spec)}, f.Type)
				}
			}
		}
	case *ast.CallExpr:
		for _, t := range s.call(c, e) {
			if t.lit == nil && t.c.fn.Type.Results != nil {
				return s.typeExpr(t.c, t.c.fn.Type.Results.List[0].Type)
			}
		}
		if pkg, _ := s.stdCall(c, e); pkg != "" && s.pkgs[pkg] == nil {
			return typeRef{"", pkg + "."} // what another module's function returns
		}
	case *ast.StarExpr:
		return s.typeOf(c, e.X, depth+1)
	case *ast.UnaryExpr:
		return s.typeOf(c, e.X, depth+1)
	case *ast.CompositeLit:
		return s.typeExpr(c, e.Type)
	case *ast.TypeAssertExpr:
		if _, ok := e.Type.(*ast.InterfaceType); ok {
			return s.typeOf(c, e.X, depth+1) // what implements the asserted value's interface
		}
		return s.typeExpr(c, e.Type)
	}
	return typeRef{}
}

// typeExpr names the type a type expression of a file declares.
func (s *routerSource) typeExpr(c scope, e ast.Expr) typeRef {
	switch e := e.(type) {
	case *ast.StarExpr:
		return s.typeExpr(c, e.X)
	case *ast.Ident:
		if s.pkgs[c.dir].types[e.Name] != nil {
			return typeRef{c.dir, e.Name}
		}
		return typeRef{"", e.Name}
	case *ast.SelectorExpr:
		if dir, path, ok := s.imported(c, e.X); ok && s.pkgs[dir] != nil {
			return typeRef{dir, e.Sel.Name}
		} else if ok {
			return typeRef{"", path + "." + e.Sel.Name}
		}
	}
	return typeRef{}
}

// imported resolves a package name of a file: the package's directory in this module, or its
// import path.
func (s *routerSource) imported(c scope, x ast.Expr) (dir, importPath string, ok bool) {
	id, isIdent := x.(*ast.Ident)
	if !isIdent || c.file == nil || c.fn != nil && s.local(c, id.Name) {
		return "", "", false
	}
	for _, imp := range c.file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == id.Name {
			return strings.TrimPrefix(p, "github.com/HyperPteLtd/openwrt-hyper/"), p, true
		}
	}
	return "", "", false
}

// local reports whether a name is a receiver, parameter, result or variable of a function.
func (s *routerSource) local(c scope, name string) bool {
	t := c.fn.Type
	return paramField(c.fn.Recv, name) != nil || paramField(t.Params, name) != nil || paramField(t.Results, name) != nil ||
		len(s.assignments(c, name)) > 0
}

func paramField(list *ast.FieldList, name string) *ast.Field {
	if list == nil {
		return nil
	}
	for _, f := range list.List {
		for _, n := range f.Names {
			if n.Name == name {
				return f
			}
		}
	}
	return nil
}

func paramIndex(list *ast.FieldList, name string) int {
	i := 0
	for _, f := range list.List {
		for _, n := range f.Names {
			if n.Name == name {
				return i
			}
			i++
		}
	}
	return -1
}

type assignment struct {
	rhs   ast.Expr // nil for a variable declared without a value
	index int      // which result of a call, when one call sets several names
	typ   ast.Expr // the declared type
	tok   token.Token
}

// assignments returns what a function assigns to a name, in its body and its function literals.
func (s *routerSource) assignments(c scope, name string) []assignment {
	var out []assignment
	if c.fn == nil {
		return nil
	}
	ast.Inspect(c.fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				if isIdent(l) && exprKey(l) == name {
					if len(n.Rhs) == len(n.Lhs) {
						out = append(out, assignment{n.Rhs[i], 0, nil, n.Tok})
					} else {
						out = append(out, assignment{n.Rhs[0], i, nil, n.Tok})
					}
				}
			}
		case *ast.ValueSpec:
			for i, n2 := range n.Names {
				if a := (assignment{typ: n.Type, tok: token.DEFINE}); n2.Name == name {
					if i < len(n.Values) {
						a.rhs = n.Values[i]
					}
					out = append(out, a)
				}
			}
		case *ast.RangeStmt:
			if exprKey(n.Key) == name || exprKey(n.Value) == name {
				out = append(out, assignment{n.X, 1, nil, token.RANGE})
			}
		}
		return true
	})
	return out
}

func isIdent(e ast.Expr) bool {
	_, ok := e.(*ast.Ident)
	return ok
}

// Values

// values returns the strings e can hold, or false when part of it is computed at run time.
func (s *routerSource) values(c scope, e ast.Expr, depth int) ([]string, bool) {
	if depth > 10 {
		return nil, false
	}
	known := c.env[exprKey(e)]
	out, ok := known.in, true
	if known.in == nil {
		out, ok = s.valuesOf(c, e, depth)
	}
	return slices.DeleteFunc(slices.Clone(out), func(v string) bool { return slices.Contains(known.out, v) }), ok
}

func (s *routerSource) valuesOf(c scope, e ast.Expr, depth int) ([]string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		v, err := strconv.Unquote(e.Value)
		return []string{v}, err == nil && e.Kind == token.STRING
	case *ast.ParenExpr:
		return s.values(c, e.X, depth+1)
	case *ast.BinaryExpr:
		l, ok := s.values(c, e.X, depth+1)
		r, ok2 := s.values(c, e.Y, depth+1)
		// A side that can hold nothing leaves nothing to join.
		if e.Op != token.ADD || (!ok || !ok2) && !(ok && len(l) == 0 || ok2 && len(r) == 0) {
			return nil, false
		}
		var out []string
		for _, a := range l {
			for _, b := range r {
				out = append(out, a+b)
			}
		}
		return out, true
	case *ast.Ident:
		return s.ident(c, e, depth)
	case *ast.SelectorExpr:
		if dir, spec, ok := s.packageVar(c, e); ok && dir != c.dir {
			return s.specValue(dir, spec, e.Sel.Name, depth)
		}
		if v, ok := s.tableField(c, e, depth); ok {
			return v, true
		}
		return s.fieldAssignments(c, e.Sel.Name, depth)
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" && len(e.Args) == 0 {
			v, ok := s.errorVar(c, sel.X)
			return []string{v}, ok
		}
		ts := s.call(c, e)
		var out []string
		resolved := len(ts) > 0
		for _, t := range ts {
			ast.Inspect(t.body(), func(n ast.Node) bool {
				if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) > 0 {
					v, ok := s.values(t.c, r.Results[0], depth+1)
					resolved = resolved && ok
					out = append(out, v...)
				}
				_, nested := n.(*ast.FuncLit)
				return !nested
			})
		}
		return out, resolved
	}
	return nil, false
}

func (s *routerSource) specValue(dir string, spec *ast.ValueSpec, name string, depth int) ([]string, bool) {
	for i, n := range spec.Names {
		if n.Name == name && i < len(spec.Values) {
			return s.values(scope{dir: dir, file: s.fileOf(dir, spec)}, spec.Values[i], depth+1)
		}
	}
	return nil, false
}

// ident follows a name: the cases of a switch on it, the values assigned to it in its function,
// the arguments the function's callers pass for it, or a package constant.
func (s *routerSource) ident(c scope, id *ast.Ident, depth int) ([]string, bool) {
	if c.fn == nil || !s.local(c, id.Name) && s.literalParam(c, id) == nil {
		if spec := s.pkgs[c.dir].vars[id.Name]; spec != nil {
			return s.specValue(c.dir, spec, id.Name, depth)
		}
		return nil, false
	}
	if cases := switchCases(c.fn.Body, id); cases != nil {
		var out []string
		for _, e := range cases {
			v, ok := s.values(c, e, depth+1)
			if !ok {
				return nil, false
			}
			out = append(out, v...)
		}
		return out, true
	}
	if lit := s.literalParam(c, id); lit != nil {
		return s.argValues(target{c: c, lit: lit}, paramIndex(lit.Type.Params, id.Name), depth)
	}
	var out []string
	resolved := true
	for _, a := range s.assignments(c, id.Name) {
		switch {
		case a.rhs == nil: // the zero value
		case a.index != 0 || a.tok != token.DEFINE && a.tok != token.ASSIGN:
			resolved = false
		default:
			v, ok := s.values(c, a.rhs, depth+1)
			resolved = resolved && ok
			out = append(out, v...)
		}
	}
	if i := paramIndex(c.fn.Type.Params, id.Name); i >= 0 {
		v, ok := s.argValues(target{c: c}, i, depth)
		return append(out, v...), ok && resolved
	}
	return out, resolved
}

// literalParam returns the function literal whose parameter id is, if any.
func (s *routerSource) literalParam(c scope, id *ast.Ident) *ast.FuncLit {
	var lit *ast.FuncLit
	ast.Inspect(c.fn.Body, func(n ast.Node) bool {
		if l, ok := n.(*ast.FuncLit); ok && l.Pos() <= id.Pos() && id.End() <= l.End() && paramField(l.Type.Params, id.Name) != nil {
			lit = l
		}
		return true
	})
	return lit
}

// argValues resolves argument i of every call that reaches a function.
func (s *routerSource) argValues(t target, i, depth int) ([]string, bool) {
	k := argKey{t.node(), i}
	if r, ok := s.args[k]; ok {
		return r.values, r.ok
	}
	s.args[k] = result{nil, true} // a cycle adds nothing
	var out []string
	callers := s.callersOf(t)
	resolved := len(callers) > 0
	for _, k := range callers {
		if i >= len(k.call.Args) {
			resolved = false
			continue
		}
		v, ok := s.values(k.c, k.call.Args[i], depth+1)
		resolved = resolved && ok
		out = append(out, v...)
	}
	s.args[k] = result{out, resolved}
	return out, resolved
}

// switchCases returns the values of the case that holds id in a switch on id, if any.
func switchCases(body *ast.BlockStmt, id *ast.Ident) []ast.Expr {
	var found []ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		if sw, ok := n.(*ast.SwitchStmt); ok && exprKey(sw.Tag) == id.Name {
			for _, cc := range sw.Body.List {
				if cc := cc.(*ast.CaseClause); cc.Colon < id.Pos() && id.End() <= cc.End() && len(cc.List) > 0 {
					found = cc.List
				}
			}
		}
		return found == nil
	})
	return found
}

// tableField resolves x.f in a loop over a table, for _, x := range table, written in place or
// held by a variable of the function or the package.
func (s *routerSource) tableField(c scope, sel *ast.SelectorExpr, depth int) ([]string, bool) {
	var out []string
	resolved := false
	ast.Inspect(c.fn.Body, func(n ast.Node) bool {
		r, ok := n.(*ast.RangeStmt)
		if !ok || exprKey(r.Value) != exprKey(sel.X) || !isIdent(sel.X) {
			return true
		}
		tc, table := s.composite(c, r.X)
		arr, ok := table.Type.(*ast.ArrayType)
		if !ok {
			return true
		}
		st, ok := arr.Elt.(*ast.StructType)
		if spec := s.pkgs[tc.dir].types[baseType(arr.Elt)]; !ok && spec != nil {
			st, ok = spec.Type.(*ast.StructType)
		}
		if !ok {
			return true
		}
		position, i := -1, 0
		for _, f := range st.Fields.List {
			for _, n := range f.Names {
				if n.Name == sel.Sel.Name {
					position = i
				}
				i++
			}
		}
		resolved = true
		for _, e := range table.Elts {
			row, ok := e.(*ast.CompositeLit)
			vs, known := s.values(tc, field(row, sel.Sel.Name, position), depth+1)
			resolved = resolved && ok && known
			out = append(out, vs...)
		}
		return false
	})
	return out, resolved
}

// composite returns the composite literal an expression is or a variable holds, with its scope.
func (s *routerSource) composite(c scope, e ast.Expr) (scope, *ast.CompositeLit) {
	switch e := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		return c, e
	case *ast.Ident:
		if c.fn != nil && s.local(c, e.Name) {
			for _, a := range s.assignments(c, e.Name) {
				if lit, ok := a.rhs.(*ast.CompositeLit); ok {
					return c, lit
				}
			}
		} else if spec := s.pkgs[c.dir].vars[e.Name]; spec != nil && len(spec.Values) == len(spec.Names) {
			i := slices.IndexFunc(spec.Names, func(n *ast.Ident) bool { return n.Name == e.Name })
			return s.composite(scope{dir: c.dir, file: s.fileOf(c.dir, spec)}, spec.Values[i])
		}
	}
	return c, &ast.CompositeLit{}
}

// fieldAssignments resolves a struct field by the values assigned to it in the package.
func (s *routerSource) fieldAssignments(c scope, name string, depth int) ([]string, bool) {
	var out []string
	found, resolved := false, true
	for _, f := range s.pkgs[c.dir].files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if a, ok := n.(*ast.AssignStmt); ok && len(a.Lhs) == len(a.Rhs) {
					for i, l := range a.Lhs {
						if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
							found = true
							v, ok := s.values(scope{dir: c.dir, file: f, fn: fn}, a.Rhs[i], depth+1)
							resolved = resolved && ok
							out = append(out, v...)
						}
					}
				}
				return true
			})
		}
	}
	return out, found && resolved
}

// field returns a composite literal's field by key, or by position when unkeyed.
func field(lit *ast.CompositeLit, name string, position int) ast.Expr {
	for i, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok && exprKey(kv.Key) == name {
			return kv.Value
		} else if !ok && i == position {
			return e
		}
	}
	return nil
}

// exprKey names a variable, or a field of one, by its source.
func exprKey(e ast.Expr) string {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x := exprKey(e.X); x != "" {
			return x + "." + e.Sel.Name
		}
	}
	return ""
}

// Paths

// alongPaths resolves an expression along each path to it: through the checks that leave the
// function unless a name holds certain values, the condition of an if it is in, and each case
// of a switch before it that does not return.
func (s *routerSource) alongPaths(c scope, e ast.Expr) ([]string, bool) {
	var out []string
	envs := s.paths(c, c.fn.Body.List, e.Pos(), []map[string]constraint{{}})
	for _, env := range envs {
		c.env = env
		v, ok := s.values(c, e, 0)
		if !ok {
			return nil, false
		}
		out = append(out, v...)
	}
	return out, len(envs) > 0
}

func (s *routerSource) paths(c scope, list []ast.Stmt, pos token.Pos, envs []map[string]constraint) []map[string]constraint {
	for _, st := range list {
		if st.Pos() > pos {
			break
		}
		if pos < st.End() {
			// Into the block or case that holds it, past the condition of an if.
			var inner []ast.Stmt
			ast.Inspect(st, func(n ast.Node) bool {
				if inner != nil || n == nil || pos < n.Pos() || n.End() <= pos {
					return false
				}
				switch n := n.(type) {
				case *ast.IfStmt:
					if n.Body.Pos() <= pos && pos < n.Body.End() {
						for i := range envs {
							envs[i] = s.narrow(c, envs[i], n.Cond, true)
						}
						inner = n.Body.List
					}
				case *ast.BlockStmt:
					inner = n.List
				case *ast.CaseClause:
					inner = n.Body
				}
				return inner == nil
			})
			if inner == nil {
				return envs
			}
			return s.paths(c, inner, pos, envs)
		}
		switch st := st.(type) {
		case *ast.IfStmt:
			for i := range envs {
				if st.Else == nil && returns(st.Body.List) {
					envs[i] = s.narrow(c, envs[i], st.Cond, false)
				}
			}
		case *ast.SwitchStmt:
			tag := exprKey(st.Tag)
			if tag == "" {
				continue
			}
			var next []map[string]constraint
			var all []string
			hasDefault := false
			for _, cc := range st.Body.List {
				cc := cc.(*ast.CaseClause)
				var values []string
				for _, e := range cc.List {
					v, _ := s.values(c, e, 0)
					values = append(values, v...)
				}
				all, hasDefault = append(all, values...), hasDefault || cc.List == nil
				for _, env := range envs {
					if !returns(cc.Body) {
						next = append(next, s.paths(c, cc.Body, cc.End(), []map[string]constraint{with(env, tag, constraint{in: values})})...)
					}
				}
			}
			for _, env := range envs {
				if !hasDefault {
					next = append(next, with(env, tag, constraint{out: all}))
				}
			}
			envs = next
		}
	}
	return envs
}

// narrow adds what a condition's truth tells of the names it tests.
func (s *routerSource) narrow(c scope, env map[string]constraint, cond ast.Expr, truth bool) map[string]constraint {
	switch e := ast.Unparen(cond).(type) {
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return s.narrow(c, env, e.X, !truth)
		}
	case *ast.BinaryExpr:
		if e.Op == token.LAND && truth || e.Op == token.LOR && !truth {
			return s.narrow(c, s.narrow(c, env, e.X, truth), e.Y, truth)
		}
		if e.Op == token.LAND && !truth {
			// None of several differences holds: one of their values.
			var in []string
			var name string
			for _, part := range flatten(e) {
				b, ok := part.(*ast.BinaryExpr)
				if !ok || b.Op != token.NEQ || name != "" && name != exprKey(b.X) {
					return env
				}
				v, _ := s.values(c, b.Y, 0)
				name, in = exprKey(b.X), append(in, v...)
			}
			return with(env, name, constraint{in: in})
		}
		if v, ok := s.values(c, e.Y, 0); ok && exprKey(e.X) != "" && (e.Op == token.EQL || e.Op == token.NEQ) {
			if (e.Op == token.EQL) == truth {
				return with(env, exprKey(e.X), constraint{in: v})
			}
			return with(env, exprKey(e.X), constraint{out: v})
		}
	case *ast.CallExpr:
		// slices.Contains(list, x)
		if pkg, name := s.stdCall(c, e); pkg == "slices" && name == "Contains" && truth {
			return with(env, exprKey(e.Args[1]), constraint{in: s.elements(c, e.Args[0], false)})
		}
	case *ast.IndexExpr:
		// set[x], for a map that holds true for each key it has
		if truth {
			return with(env, exprKey(e.Index), constraint{in: s.elements(c, e.X, true)})
		}
	}
	return env
}

// elements returns the values of a list literal's elements, or of a map literal's keys.
func (s *routerSource) elements(c scope, e ast.Expr, keys bool) []string {
	lc, list := s.composite(c, e)
	out := []string{}
	for _, el := range list.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok && keys {
			el = kv.Key
		}
		v, _ := s.values(lc, el, 0)
		out = append(out, v...)
	}
	return out
}

func flatten(e ast.Expr) []ast.Expr {
	if b, ok := ast.Unparen(e).(*ast.BinaryExpr); ok && b.Op == token.LAND {
		return append(flatten(b.X), flatten(b.Y)...)
	}
	return []ast.Expr{e}
}

// with returns env with what is known of a name narrowed by c.
func with(env map[string]constraint, name string, c constraint) map[string]constraint {
	if name == "" {
		return env
	}
	out := map[string]constraint{}
	for k, v := range env {
		out[k] = v
	}
	old := out[name]
	if c.in == nil {
		c.in = old.in
	} else if old.in != nil {
		c.in = slices.DeleteFunc(slices.Clone(c.in), func(v string) bool { return !slices.Contains(old.in, v) })
	}
	c.out = append(slices.Clone(old.out), c.out...)
	out[name] = c
	return out
}

// returns reports whether a list of statements ends by leaving its function.
func returns(list []ast.Stmt) bool {
	if len(list) == 0 {
		return false
	}
	_, ok := list[len(list)-1].(*ast.ReturnStmt)
	return ok
}

// The helpers

// sentinels returns the messages of the errors subscriptioncli.code reports by a code of its own.
func (s *routerSource) sentinels(t *testing.T) map[string]bool {
	out := map[string]bool{}
	code := s.pkgs["internal/cli/subscriptioncli"].funcs["code"]
	c := s.declScope("internal/cli/subscriptioncli", code)
	ast.Inspect(code.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if v, ok := s.errorVar(c, sel); ok {
				out[v] = true
			}
		}
		return true
	})
	if len(out) == 0 {
		t.Fatal("subscriptioncli.code classifies no subscription errors")
	}
	return out
}

// englishErrors reports whether an English text is an error the router's Go code or shell
// helpers can still produce: a string literal, a format with its verbs filled in, the text of a
// helper script, or a context error.
func (s *routerSource) englishErrors(t *testing.T) func(string) bool {
	literals := map[string]bool{context.DeadlineExceeded.Error(): true, context.Canceled.Error(): true}
	var formats []*regexp.Regexp
	verb := regexp.MustCompile(`%[sdvqw]`)
	for v := range s.literals {
		if verb.MatchString(v) {
			formats = append(formats, regexp.MustCompile("^"+verb.ReplaceAllString(regexp.QuoteMeta(v), `\S+`)+"$"))
		} else {
			literals[v] = true
		}
	}
	var scripts strings.Builder
	for _, dir := range []string{"../../files/usr/libexec/hyper", "../../packaging/openwrt"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && !e.IsDir() {
				scripts.Write(b)
			}
		}
	}
	return func(text string) bool {
		return literals[text] || strings.Contains(scripts.String(), text) ||
			slices.ContainsFunc(formats, func(re *regexp.Regexp) bool { return re.MatchString(text) })
	}
}

// The pages

type luciText struct {
	maps     map[string]map[string]bool
	dom      string
	sources  string // pages/common.js
	patterns []*regexp.Regexp
	lines    map[string]*regexp.Regexp // diagnosticMessage's own: dns, network, wrapped
}

// readLuci reads the keys of the LuCI pages' text maps, and how diagnosticMessage reads a text.
func readLuci(t *testing.T) *luciText {
	t.Helper()
	l := &luciText{maps: map[string]map[string]bool{}, lines: map[string]*regexp.Regexp{}}
	for file, names := range map[string][]string{
		"dom.js":           {"diagnosticText", "errorText", "providerErrors", "kernelErrors", "taskText", "taskStep", "taskStage"},
		"pages/common.js":  {"notices", "pendingUpdates"},
		"pages/records.js": {"sources"},
	} {
		b, err := os.ReadFile(luci + file)
		if err != nil {
			t.Fatal(err)
		}
		if file == "dom.js" {
			l.dom = string(b)
		} else if file == "pages/common.js" {
			l.sources = string(b)
		}
		for _, name := range names {
			keys, err := objectKeys(string(b), name)
			if err != nil || len(keys) == 0 {
				t.Fatalf("%s %s: %v", file, name, err)
			}
			l.maps[name] = keys
		}
	}
	l.patterns = errorPatterns(t, l.dom)
	for _, name := range []string{"dns", "network", "wrapped"} {
		m := regexp.MustCompile(`const ` + name + ` = /((?:\\.|[^/\\\n])+)/([a-z]*)\.exec\(text\)`).FindStringSubmatch(l.dom)
		if m == nil {
			t.Fatalf("dom.js: diagnosticMessage's %s line not found", name)
		}
		l.lines[name] = jsRegexp(m[1], m[2])
	}
	return l
}

// readable reports whether a code of a kind reads in Chinese.
func (l *luciText) readable(kind, code string) bool {
	for _, m := range readers[kind] {
		if l.maps[m][code] {
			return true
		}
	}
	// noticeRows handles some notices by their code.
	if kind == "notice" && strings.Contains(l.sources, "'"+code+"'") {
		return true
	}
	return kind != "source" && l.family(code)
}

// family reports whether a pattern of diagnosticMessage reads a text: an errorPatterns entry, a
// DNS issue of a dnsmasq instance, a network helper's failed step, or a known error after a
// prefix ("<what>: <error>").
func (l *luciText) family(text string) bool {
	if slices.ContainsFunc(l.patterns, func(re *regexp.Regexp) bool { return re.MatchString(text) }) {
		return true
	}
	if m := l.lines["dns"].FindStringSubmatch(text); m != nil && l.maps["diagnosticText"][m[1]] {
		return true
	}
	m := l.lines["wrapped"].FindStringSubmatch(text)
	return l.lines["network"].MatchString(text) || m != nil && (l.maps["errorText"][m[2]] || l.maps["diagnosticText"][m[2]])
}

// objectKeys reads the keys of `const name = { key: value, ... };`.
func objectKeys(src, name string) (map[string]bool, error) {
	_, rest, ok := strings.Cut(src, "const "+name+" = {")
	if !ok {
		return nil, os.ErrNotExist
	}
	keys := map[string]bool{}
	for i := 0; ; {
		i = skipSpace(rest, i)
		if rest[i] == '}' {
			return keys, nil
		}
		var key string
		if q := rest[i]; q == '\'' || q == '"' {
			end := strings.IndexByte(rest[i+1:], q)
			key, i = rest[i+1:i+1+end], i+end+2
		} else {
			end := i
			for end < len(rest) && (rest[end] == '_' || rest[end] >= 'a' && rest[end] <= 'z' || rest[end] >= 'A' && rest[end] <= 'Z' || rest[end] >= '0' && rest[end] <= '9') {
				end++
			}
			key, i = rest[i:end], end
		}
		if i = skipSpace(rest, i); key == "" || rest[i] != ':' {
			return nil, strconv.ErrSyntax
		}
		keys[key] = true
		// The value runs to a comma or the closing brace outside strings and brackets.
		for depth := 0; ; {
			i++
			switch ch := rest[i]; ch {
			case '\'', '"', '`':
				for i++; rest[i] != ch; i++ {
					if rest[i] == '\\' {
						i++
					}
				}
			case '(', '[', '{':
				depth++
			case ')', ']':
				depth--
			case '}':
				if depth == 0 {
					return keys, nil
				}
				depth--
			}
			if rest[i] == ',' && depth == 0 {
				i++
				break
			}
		}
	}
}

func skipSpace(src string, i int) int {
	for {
		for i < len(src) && strings.ContainsRune(" \t\r\n", rune(src[i])) {
			i++
		}
		if !strings.HasPrefix(src[i:], "//") {
			return i
		}
		i += strings.IndexByte(src[i:], '\n')
	}
}

// errorPatterns compiles dom.js's errorPatterns, the families of errors diagnosticMessage reads.
func errorPatterns(t *testing.T, dom string) []*regexp.Regexp {
	t.Helper()
	_, list, _ := strings.Cut(dom, "const errorPatterns = [")
	list, _, _ = strings.Cut(list, "\n];")
	var out []*regexp.Regexp
	for _, m := range regexp.MustCompile(`(?m)^\s*\[/((?:\\.|[^/\\\n])+)/([a-z]*),`).FindAllStringSubmatch(list, -1) {
		out = append(out, jsRegexp(m[1], m[2]))
	}
	if len(out) == 0 {
		t.Fatal("dom.js errorPatterns not found")
	}
	return out
}

func jsRegexp(source, flags string) *regexp.Regexp {
	if strings.Contains(flags, "s") {
		source = "(?s)" + source
	}
	return regexp.MustCompile(source)
}
