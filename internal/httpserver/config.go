package httpserver

type Config struct {
	LocalAddress string
	GinMode      string
	Cors         CorsConfig
	Static       StaticConfig
	Options      []string
	// TrustedProxies lists the CIDRs whose X-Forwarded-For and X-Real-IP headers
	// may be believed. Empty means believe nobody, and the client address is the
	// peer that actually opened the connection.
	//
	// Gin's own default is to trust every proxy, which makes the reported client
	// address a header the caller writes. Anything keyed by it — the login
	// throttle above all — is then keyed by a value the attacker chooses, and
	// bounds nothing. An operator running behind a reverse proxy has to name it
	// here for the real client address to survive the hop.
	TrustedProxies []string
}

type CorsConfig struct {
	// AllowedOrigins is a list of origins a cross-domain request can be executed from.
	// If the special "*" value is present in the list, all origins will be allowed.
	// An origin may contain a wildcard (*) to replace 0 or more characters
	// (i.e.: http://*.domain.com). Usage of wildcards implies a small performance penalty.
	// Only one wildcard can be used per origin.
	// Empty (the default) denies cross-origin browser access.
	AllowedOrigins []string
	// AllowedMethods is a list of methods the client is allowed to use with
	// cross-domain requests. Default value is simple methods (HEAD, GET and POST).
	AllowedMethods []string
	// AllowedHeaders is list of non simple headers the client is allowed to use with
	// cross-domain requests.
	// If the special "*" value is present in the list, all headers will be allowed.
	// Default value is [].
	AllowedHeaders []string
	// ExposedHeaders indicates which headers are safe to expose to the API of a CORS
	// API specification
	ExposedHeaders []string
	// MaxAge indicates how long (in seconds) the results of a preflight request
	// can be cached. Default value is 0, which stands for no
	// Access-Control-Max-Age header to be sent back, resulting in browsers
	// using their default value (5s by spec). If you need to force a 0 max-age,
	// set `MaxAge` to a negative value (ie: -1).
	MaxAge int
	// AllowCredentials indicates whether the request can include user credentials like
	// cookies, HTTP authentication or client side SSL certificates.
	AllowCredentials bool
	// AllowPrivateNetwork indicates whether to accept cross-origin requests over a
	// private network.
	AllowPrivateNetwork bool
	// OptionsPassthrough instructs preflight to let other potential next handlers to
	// process the OPTIONS method. Turn this on if your application handles OPTIONS.
	OptionsPassthrough bool
	// Provides a status code to use for successful OPTIONS requests.
	// Default value is http.StatusNoContent (204).
	OptionsSuccessStatus int
	// Debugging flag adds additional output to debug server side CORS issues
	Debug bool
}

// StaticConfig mounts the bundled UI or an alternative UI from disk.
type StaticConfig struct {
	// Bundled enables the embedded Elm UI when Dir is empty and the executable
	// was built with the webui tag. Backend-only builds carry no embedded assets.
	Bundled bool
	// Dir overrides the bundled UI with a directory on disk. A configured
	// directory that does not exist is a startup error.
	Dir string
	// Path is where it mounts. It cannot be "/": a root wildcard conflicts with
	// the API routes and makes gin panic at startup.
	Path string
}

func NewDefaultConfig() Config {
	return Config{
		LocalAddress: ":3333",
		GinMode:      "release",
		Cors: CorsConfig{
			AllowedOrigins: []string{},
			// Only the headers the server actually reads. "*" reflected back whatever
			// a caller asked for, which grants more than anything here needs.
			// Literals rather than the constants that define them: those live in
			// packages that import this one.
			AllowedHeaders: []string{
				"Content-Type",
				"Authorization", // http_auth.AuthorizationHeader
				"X-Api-Key",     // torznab api key header
			},
		},
		Static: StaticConfig{
			Bundled: true,
			Path:    "/ui",
		},
		Options: []string{"*"},
	}
}
