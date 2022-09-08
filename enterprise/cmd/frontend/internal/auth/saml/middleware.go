package saml

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sourcegraph/log"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/auth"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/auth/providers"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/external/session"
	"github.com/sourcegraph/sourcegraph/internal/actor"
	"github.com/sourcegraph/sourcegraph/internal/database"
)

// All SAML endpoints are under this path prefix.
const authPrefix = auth.AuthURLPrefix + "/saml"

// Middleware is middleware for SAML authentication, adding endpoints under the auth path prefix to
// enable the login flow an requiring login for all other endpoints.
//
// 🚨 SECURITY
func Middleware(logger log.Logger, db database.DB) *auth.Middleware {
	logger = logger.Scoped("saml.middleware", "middleware that handles SAML authentication")
	return &auth.Middleware{
		API: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authHandler(logger.Scoped("saml.api", "api handler for SAML middleware"), db, w, r, next, true)
			})
		},
		App: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authHandler(logger.Scoped("saml.app", "app handler for SAML middleware"), db, w, r, next, false)
			})
		},
	}
}

// authHandler is the new SAML HTTP auth handler.
//
// It uses github.com/russelhaering/gosaml2 and (unlike authHandler1) makes it possible to support
// multiple auth providers with SAML and expose more SAML functionality.
func authHandler(logger log.Logger, db database.DB, w http.ResponseWriter, r *http.Request, next http.Handler, isAPIRequest bool) {
	// Delegate to SAML ACS and metadata endpoint handlers.
	if !isAPIRequest && strings.HasPrefix(r.URL.Path, auth.AuthURLPrefix+"/saml/") {
		samlSPHandler(logger, db)(w, r)
		return
	}

	// If the actor is authenticated and not performing a SAML operation, then proceed to next.
	if actor.FromContext(r.Context()).IsAuthenticated() {
		next.ServeHTTP(w, r)
		return
	}

	// If there is only one auth provider configured, the single auth provider is SAML, and it's an
	// app request, redirect to signin immediately. The user wouldn't be able to do anything else
	// anyway; there's no point in showing them a signin screen with just a single signin option.
	if ps := providers.Providers(); len(ps) == 1 && ps[0].Config().Saml != nil && !isAPIRequest {
		p, handled := handleGetProvider(r.Context(), logger, w, ps[0].ConfigID().ID)
		if handled {
			return
		}
		redirectToAuthURL(logger, w, r, p, auth.SafeRedirectURL(r.URL.String()))
		return
	}

	next.ServeHTTP(w, r)
}

func samlSPHandler(logger log.Logger, db database.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		requestPath := strings.TrimPrefix(r.URL.Path, authPrefix)

		// Handle GET endpoints.
		if r.Method == "GET" {
			// All of these endpoints expect the provider ID in the URL query.
			p, handled := handleGetProvider(r.Context(), logger, w, r.URL.Query().Get("pc"))
			if handled {
				return
			}

			switch requestPath {
			case "/metadata":
				metadata, err := p.samlSP.Metadata()
				if err != nil {
					logger.Error("Error generating SAML service provider metadata.", log.Error(err))
					http.Error(w, "", http.StatusInternalServerError)
					return
				}

				buf, err := xml.MarshalIndent(metadata, "", "  ")
				if err != nil {
					logger.Error("Error encoding SAML service provider metadata.", log.Error(err))
					http.Error(w, "", http.StatusInternalServerError)
					return
				}
				traceLog(fmt.Sprintf("Service Provider metadata: %s", p.ConfigID().ID), string(buf))
				w.Header().Set("Content-Type", "application/samlmetadata+xml; charset=utf-8")
				_, _ = w.Write(buf)
				return

			case "/login":
				// It is safe to use r.Referer() because the redirect-to URL will be checked later,
				// before the client is actually instructed to navigate there.
				redirectToAuthURL(logger, w, r, p, r.Referer())
				return
			}
		}

		if r.Method != "POST" {
			http.Error(w, "", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "", http.StatusBadRequest)
			return
		}

		// The remaining endpoints all expect the provider ID in the POST data's RelayState.
		traceLog("SAML RelayState", r.FormValue("RelayState"))
		var relayState relayState
		relayState.decode(r.FormValue("RelayState"))

		p, handled := handleGetProvider(r.Context(), logger, w, relayState.ProviderID)
		if handled {
			return
		}

		// Handle POST endpoints.
		switch requestPath {
		case "/acs":
			info, err := readAuthnResponse(p, r.FormValue("SAMLResponse"))
			if err != nil {
				logger.Error("Error validating SAML assertions. Set the env var INSECURE_SAML_LOG_TRACES=1 to log all SAML requests and responses.", log.Error(err))
				http.Error(w, "Error validating SAML assertions. Try signing in again. If the problem persists, a site admin must check the configuration.", http.StatusForbidden)
				return
			}

			if !allowSignin(p, info.groups) {
				groups := make([]string, 0, len(info.groups))
				for k := range info.groups {
					groups = append(groups, k)
				}
				logger.Warn("Error authorizing SAML-authenticated user.", log.String("AccountID", info.spec.AccountID), log.Strings("Expected groups", p.config.AllowGroups), log.Strings("Got", groups))
				http.Error(w, "Error authorizing SAML-authenticated user. The user does not belong to one of the configured groups.", http.StatusForbidden)
				return
			}
			allowSignup := (p.config.AllowSignup == nil || *p.config.AllowSignup)
			actor, safeErrMsg, err := getOrCreateUser(r.Context(), db, allowSignup, info)
			if err != nil {
				logger.Error("Error looking up SAML-authenticated user.", log.Error(err))
				http.Error(w, safeErrMsg, http.StatusInternalServerError)
				return
			}

			user, err := db.Users().GetByID(r.Context(), actor.UID)
			if err != nil {
				logger.Error("Error retrieving SAML-authenticated user from database.", log.Error(err))
				http.Error(w, "Failed to retrieve user: "+err.Error(), http.StatusInternalServerError)
				return
			}

			var exp time.Duration
			// 🚨 SECURITY: TODO(sqs): We *should* uncomment the line below to make our own sessions
			// only last for as long as the IdP said the authn grant is active for. Unfortunately,
			// until we support refreshing SAML authn in the background
			// (https://github.com/sourcegraph/sourcegraph/issues/11340), this provides a bad user
			// experience because users need to re-authenticate via SAML every minute or so
			// (assuming their SAML IdP, like many, has a 1-minute access token validity period).
			//
			// if info.SessionNotOnOrAfter != nil {
			// 	exp = time.Until(*info.SessionNotOnOrAfter)
			// }
			if err := session.SetActor(w, r, actor, exp, user.CreatedAt); err != nil {
				logger.Error("Error setting SAML-authenticated actor in session.", log.Error(err))
				http.Error(w, "Error starting SAML-authenticated session. Try signing in again.", http.StatusInternalServerError)
				return
			}

			// 🚨 SECURITY: Call auth.SafeRedirectURL to avoid an open-redirect vuln.
			http.Redirect(w, r, auth.SafeRedirectURL(relayState.ReturnToURL), http.StatusFound)

		case "/logout":
			encodedResp := r.FormValue("SAMLResponse")

			{
				if raw, err := base64.StdEncoding.DecodeString(encodedResp); err == nil {
					traceLog(fmt.Sprintf("LogoutResponse: %s", p.ConfigID().ID), string(raw))
				}
			}

			// TODO(sqs): Fully validate the LogoutResponse here (i.e., also validate that the document
			// is a valid LogoutResponse). It is possible that this request is being spoofed, but it
			// doesn't let an attacker do very much (just log a user out and redirect).
			//
			// 🚨 SECURITY: If this logout handler starts to do anything more advanced, it probably must
			// validate the LogoutResponse to avoid being vulnerable to spoofing.
			_, err := p.samlSP.ValidateEncodedResponse(encodedResp)
			if err != nil && !strings.HasPrefix(err.Error(), "unable to unmarshal response:") {
				logger.Error("Error validating SAML logout response.", log.Error(err))
				http.Error(w, "Error validating SAML logout response.", http.StatusForbidden)
				return
			}

			// If this is an SP-initiated logout, then the actor has already been cleared from the
			// session (but there's no harm in clearing it again). If it's an IdP-initiated logout,
			// then it hasn't, and we must clear it here.
			if err := session.SetActor(w, r, nil, 0, time.Time{}); err != nil {
				logger.Error("Error clearing actor from session in SAML logout handler.", log.Error(err))
				http.Error(w, "Error signing out of SAML-authenticated session.", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/", http.StatusFound)

		default:
			http.Error(w, "", http.StatusNotFound)
		}
	}
}

func redirectToAuthURL(logger log.Logger, w http.ResponseWriter, r *http.Request, p *provider, returnToURL string) {
	authURL, err := buildAuthURLRedirect(p, relayState{
		ProviderID:  p.ConfigID().ID,
		ReturnToURL: auth.SafeRedirectURL(returnToURL),
	})
	if err != nil {
		logger.Error("Failed to build SAML auth URL.", log.Error(err))
		http.Error(w, "Unexpected error in SAML authentication provider.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func buildAuthURLRedirect(p *provider, relayState relayState) (string, error) {
	doc, err := p.samlSP.BuildAuthRequestDocument()
	if err != nil {
		return "", err
	}
	{
		if data, err := doc.WriteToString(); err == nil {
			traceLog(fmt.Sprintf("AuthnRequest: %s", p.ConfigID().ID), data)
		}
	}
	return p.samlSP.BuildAuthURLRedirect(relayState.encode(), doc)
}

// relayState represents the decoded RelayState value in both the IdP-initiated and SP-initiated
// login flows.
//
// SAML overloads the term "RelayState".
//   - In the SP-initiated login flow, it is an opaque value originated from the SP and reflected
//     back in the AuthnResponse. The Sourcegraph SP uses the base64-encoded JSON of this struct as
//     the RelayState.
//   - In the IdP-initiated login flow, the RelayState can be any arbitrary hint, but in practice
//     is the desired post-login redirect URL in plain text.
type relayState struct {
	ProviderID  string `json:"k"`
	ReturnToURL string `json:"r"`
}

// encode returns the base64-encoded JSON representation of the relay state.
func (s *relayState) encode() string {
	b, _ := json.Marshal(s)
	return base64.StdEncoding.EncodeToString(b)
}

// Decode decodes the base64-encoded JSON representation of the relay state into the receiver.
func (s *relayState) decode(encoded string) {
	if strings.HasPrefix(encoded, "http://") || strings.HasPrefix(encoded, "https://") || encoded == "" {
		s.ProviderID, s.ReturnToURL = "", encoded
		return
	}

	if b, err := base64.StdEncoding.DecodeString(encoded); err == nil {
		if err := json.Unmarshal(b, s); err == nil {
			return
		}
	}

	s.ProviderID, s.ReturnToURL = "", ""
}

func allowSignin(p *provider, groups map[string]bool) bool {
	if p.config.AllowGroups == nil {
		return true
	}

	for _, group := range p.config.AllowGroups {
		if groups[group] {
			return true
		}
	}
	return false
}
