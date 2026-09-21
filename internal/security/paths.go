package security

import "strings"

// Built-in probe and scanner path prefixes commonly hit by automated scanners.
var builtinProbePaths = []string{
	"/wp-admin", "/wp-login", "/wp-content", "/wp-includes", "/xmlrpc.php",
	"/.env", "/.env.local", "/.env.production", "/.git", "/.svn", "/.hg", "/.DS_Store",
	"/phpmyadmin", "/pma", "/admin.php", "/administrator", "/admin/login",
	"/actuator", "/actuator/env", "/actuator/health", "/solr", "/jenkins",
	"/console", "/manager/html", "/manager/status", "/server-status", "/server-info",
	"/vendor/phpunit", "/cgi-bin", "/HNAP1", "/sdk", "/evox/about",
	"/boaform", "/shell", "/GponForm", "/setup.cgi", "/config.json",
	"/api/v1/pods", "/_all_dbs", "/containers/json", "/v2/_catalog",
	"/debug/pprof", "/debug/vars", "/metrics", // note: real metrics on other bind
	"/telescope", "/horizon", "/_profiler", "/phpinfo.php", "/info.php",
	"/backup.sql", "/dump.sql", "/db.sql", "/database.sql",
	"/web.config", "/crossdomain.xml", "/elmah.axd", "/trace.axd",
	"/.aws/credentials", "/aws.yml", "/secrets.yml", "/docker-compose.yml",
	"/autoload_classmap.php", "/wp-config.php", "/config.php.bak",
	"/owa/auth/logon.aspx", "/remote/login", "/vpn/index.html",
	"/api/graphql", "/graphql/console", "/altair", "/playground",
	"/favicon.ico.ico", "/robots.txt.bak",
	"/.well-known/security.txt.bak", "/.vscode", "/.idea",
	"/wp-json/wp/v2/users", "/rest/V1/", "/magento_version",
	"/cgi-bin/luci", "/cgi-bin/admin.cgi", "/remote/fgt_lang",
	"/autodiscover/autodiscover.xml", "/ecp/", "/ews/",
	"/_ignition/execute-solution", "/_ignition/health-check",
	"/laravel-filemanager", "/filemanager", "/elfinder",
	"/api/sonicos", "/Telerik.Web.UI.WebResource.axd",
	"/ReportServer", "/owa/", "/exchweb/",
	"/.docker/config.json", "/proc/self/environ",
	"/etc/passwd", "/windows/win.ini",
	"/api/v2/cmdb", "/api/v1/namespaces",
	"/solr/admin", "/_cat/indices", "/_nodes", "/_cluster/health",
	"/jmx-console", "/invoker/JMXInvokerServlet",
	"/axis2-admin", "/struts", "/webtools/control/main",
	"/.env.backup", "/.env.old", "/.svn/entries", "/.svn/wc.db",
	"/actuator/heapdump", "/actuator/mappings", "/actuator/beans",
	"/actuator/logfile", "/jolokia", "/druid/index.html",
	"/wp-login.php", "/wp-content/debug.log", "/setup-config.php",
	"/storage/logs/laravel.log", "/hudson", "/login.action",
	"/swagger-ui.html", "/swagger-ui/", "/swagger.json",
	"/v2/api-docs", "/v3/api-docs", "/api-docs",
	"/nacos/v1/auth/users", "/v1/agent/self",
	"/remote/logincheck", "/SDK/webLanguage",
	"/package.json", "/composer.json", "/application.yml", "/appsettings.json",
	"/clientaccesspolicy.xml", "/sftp-config.json",
	"/host-manager/html", "/manager/text",
}

func init() {
	probePaths = append([]string{}, builtinProbePaths...)
}

// AddProbePaths appends extra path prefixes at runtime (from blocklist).
func AddProbePaths(paths ...string) {
	for _, p := range paths {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		probePaths = append(probePaths, p)
	}
}

// IsProbePath reports common scanner/prober URL patterns plus blocklist paths.
func IsProbePath(path string) bool {
	p := strings.ToLower(path)
	for _, bad := range probePaths {
		if strings.HasPrefix(p, bad) {
			return true
		}
	}
	if strings.Contains(p, "/.env") || strings.Contains(p, "/.git/") || strings.Contains(p, "/.aws/") {
		return true
	}
	if strings.HasSuffix(p, ".php") || strings.HasSuffix(p, ".asp") || strings.HasSuffix(p, ".aspx") || strings.HasSuffix(p, ".jsp") {
		if !strings.HasPrefix(p, "/v1/") && !strings.HasPrefix(p, "/openapi") && !strings.HasPrefix(p, "/docs") {
			return true
		}
	}
	if Global().MatchPath(p) {
		// Never let operator blocklists take down the real API surface.
		if strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/oauth/") || p == "/openapi.json" || strings.HasPrefix(p, "/docs") {
			return false
		}
		return true
	}
	return false
}
