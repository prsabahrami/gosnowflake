package gosnowflake

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestOauthSnowflakeAuthorizationCodeWildcardsSuccessful(t *testing.T) {
	cfg := setupOauthSnowflakeAuthorizationCodeWildcardsTest(t)
	browserCfg, err := getOauthSnowflakeAuthorizationCodeTestCredentials()
	assertNilF(t, err, fmt.Sprintf("failed to get browser config: %v", err))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		provideExternalBrowserCredentials(t, externalBrowserType.OauthSnowflakeSuccess, browserCfg.User, browserCfg.Password)
	}()
	go func() {
		defer wg.Done()
		err := verifyConnectionToSnowflakeAuthTests(t, cfg)
		assertNilE(t, err, fmt.Sprintf("Connection failed due to %v", err))
	}()
	wg.Wait()
}

func TestOauthSnowflakeAuthorizationCodeWildcardsMismatchedUsername(t *testing.T) {
	cfg := setupOauthSnowflakeAuthorizationCodeWildcardsTest(t)
	browserCfg, err := getOauthSnowflakeAuthorizationCodeTestCredentials()
	assertNilF(t, err, fmt.Sprintf("failed to get browser config: %v", err))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		provideExternalBrowserCredentials(t, externalBrowserType.OauthSnowflakeSuccess, browserCfg.User, browserCfg.Password)
	}()
	go func() {
		defer wg.Done()
		cfg.User = "fakeUser@snowflake.com"
		err := verifyConnectionToSnowflakeAuthTests(t, cfg)
		var snowflakeErr *SnowflakeError
		assertTrueF(t, errors.As(err, &snowflakeErr))
		assertEqualE(t, snowflakeErr.Number, 390309, fmt.Sprintf("Expected 390309, but got %v", snowflakeErr.Number))
	}()
	wg.Wait()
}

func TestOauthSnowflakeAuthorizationWildcardsCodeTimeout(t *testing.T) {
	cfg := setupOauthSnowflakeAuthorizationCodeWildcardsTest(t)
	cfg.ExternalBrowserTimeout = time.Duration(1) * time.Second
	err := verifyConnectionToSnowflakeAuthTests(t, cfg)
	assertNotNilF(t, err, "should failed due to timeout")
	assertEqualE(t, err.Error(), "authentication via browser timed out", fmt.Sprintf("Expecteed timeout, but got %v", err))
}

func TestOauthSnowflakeAuthorizationCodeWildcardsWithoutTokenCache(t *testing.T) {
	cfg := setupOauthSnowflakeAuthorizationCodeWildcardsTest(t)
	browserCfg, err := getOauthSnowflakeAuthorizationCodeTestCredentials()
	assertNilF(t, err, fmt.Sprintf("failed to get browser config: %v", err))
	cfg.ClientStoreTemporaryCredential = 2

	var wg sync.WaitGroup
	cfg.DisableQueryContextCache = true

	wg.Add(2)
	go func() {
		defer wg.Done()
		provideExternalBrowserCredentials(t, externalBrowserType.OauthSnowflakeSuccess, browserCfg.User, browserCfg.Password)
	}()
	go func() {
		defer wg.Done()
		err := verifyConnectionToSnowflakeAuthTests(t, cfg)
		assertNilE(t, err, fmt.Sprintf("Connection failed due to %v", err))
	}()
	wg.Wait()

	cleanupBrowserProcesses(t)
	cfg.ExternalBrowserTimeout = time.Duration(1) * time.Second

	err = verifyConnectionToSnowflakeAuthTests(t, cfg)
	assertNotNilF(t, err, "Expected an error but got nil")
	assertEqualE(t, err.Error(), "authentication via browser timed out", fmt.Sprintf("Expecteed timeout, but got %v", err))
}

func setupOauthSnowflakeAuthorizationCodeWildcardsTest(t *testing.T) *Config {
	skipAuthTests(t, "Skipping Snowflake Authorization Code tests")

	cfg, err := getAuthTestsConfig(t, AuthTypeOAuthAuthorizationCode)
	assertNilF(t, err, fmt.Sprintf("failed to get config: %v", err))

	cleanupBrowserProcesses(t)

	cfg.OauthClientID, err = GetFromEnv("SNOWFLAKE_AUTH_TEST_INTERNAL_OAUTH_SNOWFLAKE_WILDCARDS_CLIENT_ID", true)
	assertNilF(t, err, fmt.Sprintf("failed to setup config: %v", err))

	cfg.OauthClientSecret, err = GetFromEnv("SNOWFLAKE_AUTH_TEST_INTERNAL_OAUTH_SNOWFLAKE_WILDCARDS_CLIENT_SECRET", true)
	assertNilF(t, err, fmt.Sprintf("failed to setup config: %v", err))

	cfg.User, err = GetFromEnv("SNOWFLAKE_AUTH_TEST_EXTERNAL_OAUTH_OKTA_CLIENT_ID", true)
	assertNilF(t, err, fmt.Sprintf("failed to setup config: %v", err))

	cfg.Role, err = GetFromEnv("SNOWFLAKE_AUTH_TEST_INTERNAL_OAUTH_SNOWFLAKE_ROLE", true)
	assertNilF(t, err, fmt.Sprintf("failed to setup config: %v", err))

	cfg.ClientStoreTemporaryCredential = 2
	return cfg
}
