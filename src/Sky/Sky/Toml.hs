-- | Minimal sky.toml parser for Sky project configuration.
-- No external TOML library dependency — hand-written for simplicity.
module Sky.Sky.Toml where

import qualified Data.Map.Strict as Map
import Data.Char (isSpace)
import Data.List (isPrefixOf, stripPrefix)


-- | Sky project configuration
data SkyConfig = SkyConfig
    { _name          :: !String           -- project name
    , _version       :: !String           -- semver
    , _entry         :: !String           -- entry file (src/Main.sky)
    , _sourceRoot    :: !String           -- source root (src)
    , _binName       :: !String           -- output binary name (app)
    , _goDeps        :: [(String, String)]-- Go dependencies [(pkg, version)]
    , _skyDeps       :: [(String, String)]-- Sky-source dependencies [(repo, version)]
    , _livePort      :: !Int              -- [live] port (default 8000)
    , _liveStore     :: !String           -- [live] store: memory / sqlite / postgres
    , _liveStorePath :: !String           -- [live] storePath: file or connection string
    , _liveTtl       :: !Int              -- [live] ttl: session TTL in seconds
    , _liveStatic    :: !String           -- [live] static: static asset directory
    , _liveMaxBody   :: !Int              -- [live] maxBodyBytes: cap for /_sky/event POST body
    , _dbDriver      :: !String           -- [database] driver (sqlite/postgres)
    , _dbPath        :: !String           -- [database] path
    , _authSecret    :: !String           -- [auth] secret: JWT signing key
    , _authTokenTtl  :: !Int              -- [auth] tokenTtl: JWT lifetime seconds
    , _authCookie    :: !String           -- [auth] cookieName: session cookie
    , _authDriver    :: !String           -- [auth] driver: jwt / session / oauth
    , _logFormat     :: !String           -- [log] format: plain (default) | json
    , _logLevel      :: !String           -- [log] level: debug | info (default) | warn | error
    , _envPrefix     :: !String           -- [env] prefix: namespace for runtime SKY_* env reads (default "SKY")
    }
    deriving (Show)


-- | Default configuration
defaultConfig :: SkyConfig
defaultConfig = SkyConfig
    { _name          = "sky-project"
    , _version       = "0.1.0"
    , _entry         = "src/Main.sky"
    , _sourceRoot    = "src"
    , _binName       = "app"
    , _goDeps        = []
    , _skyDeps       = []
    , _livePort      = 8000
    , _liveStore     = ""
    , _liveStorePath = ""
    , _liveTtl       = 1800
    , _liveStatic    = ""
    , _liveMaxBody   = 0
    , _dbDriver      = ""
    , _dbPath        = ""
    , _authSecret    = ""
    , _authTokenTtl  = 86400
    , _authCookie    = "sky_auth"
    , _authDriver    = "jwt"
    , _logFormat     = ""
    , _logLevel      = ""
    , _envPrefix     = ""
    }


-- | Parse sky.toml content. Section-aware so [go.dependencies] entries
-- are routed into _goDeps instead of being lost.
parseSkyToml :: String -> SkyConfig
parseSkyToml content =
    let (_, cfg) = foldl applyLine ("", defaultConfig) (lines content)
    in cfg


-- | Track the current TOML section alongside the config being built.
applyLine :: (String, SkyConfig) -> String -> (String, SkyConfig)
applyLine (section, config) line =
    let trimmed = dropWhile isSpace line
    in case trimmed of
        []       -> (section, config)
        ('#':_)  -> (section, config)
        ('[':_)  ->
            let raw = takeWhile (/= ']') (drop 1 trimmed)
                name = stripQuotes (trim raw)
            in (name, config)
        _ -> case break (== '=') trimmed of
            (key, '=' : value) ->
                let k = trim key
                    v = trim (stripQuotes (trim value))
                in (section, applyKeyValue section config k v)
            _ -> (section, config)


applyKeyValue :: String -> SkyConfig -> String -> String -> SkyConfig
applyKeyValue section config key value = case section of
    "go.dependencies" ->
        config { _goDeps = _goDeps config ++ [(stripQuotes key, value)] }
    "dependencies" ->
        config { _skyDeps = _skyDeps config ++ [(stripQuotes key, value)] }
    -- [live] section: Sky.Live runtime config.
    "live" -> case key of
        "port"      -> config { _livePort = safeReadInt value (_livePort config) }
        "store"     -> config { _liveStore = value }
        "storePath" -> config { _liveStorePath = value }
        "ttl"       -> config { _liveTtl = safeReadInt value (_liveTtl config) }
        "static"    -> config { _liveStatic = value }
        -- maxBodyBytes: cap for the `/_sky/event` POST body. Default
        -- in the runtime is 5 MiB; bump higher if your app uses
        -- `Event.onFile` / `Event.onImage` with larger uploads.
        -- Seeded as SKY_LIVE_MAX_BODY_BYTES so process env still wins.
        "maxBodyBytes" -> config { _liveMaxBody = safeReadInt value (_liveMaxBody config) }
        _              -> config
    -- [database] section
    "database" -> case key of
        "driver" -> config { _dbDriver = value }
        "path"   -> config { _dbPath = value }
        _        -> config
    -- [auth] section: Std.Auth defaults. Values surface as env vars
    -- at init time so Auth.signToken / verifyToken can pick them up
    -- without each call passing a secret.
    "auth" -> case key of
        "secret"     -> config { _authSecret   = value }
        "tokenTtl"   -> config { _authTokenTtl = safeReadInt value (_authTokenTtl config) }
        "cookieName" -> config { _authCookie   = value }
        "driver"     -> config { _authDriver   = value }
        _            -> config
    -- [log] section: Std.Log defaults. Values seed SKY_LOG_FORMAT
    -- / SKY_LOG_LEVEL via SetEnvDefault at init time so env vars
    -- still win in production. Three-layer precedence (top wins):
    --   1. SKY_LOG_FORMAT / SKY_LOG_LEVEL (process env, .env file)
    --   2. sky.toml [log] format / level
    --   3. runtime defaults (plain / info)
    "log" -> case key of
        "format" -> config { _logFormat = value }
        "level"  -> config { _logLevel  = value }
        _        -> config
    -- [env] section: env-var namespace control. `prefix` overrides
    -- the default "SKY" prefix used by all internal runtime reads
    -- (LIVE_PORT, AUTH_TOKEN_TTL, LOG_FORMAT, DB_PATH, etc.). Allows
    -- multiple Sky binaries to run on the same host without env-var
    -- collision. Default unchanged ("SKY") if unset.
    "env" -> case key of
        "prefix" -> config { _envPrefix = value }
        _        -> config
    -- Top-level / [source] / [project] — project metadata.
    _ -> case key of
        "name"    -> config { _name = value }
        "version" -> config { _version = value }
        "entry"   -> config { _entry = value }
        "root"    -> config { _sourceRoot = value }
        "bin"     -> config { _binName = value }
        -- top-level `port = 8000` (legacy — before [live] section existed)
        "port"    -> config { _livePort = safeReadInt value (_livePort config) }
        _         -> config

safeReadInt :: String -> Int -> Int
safeReadInt s fallback = case reads s of
    [(n, _)] -> n
    _        -> fallback


-- Helpers

trim :: String -> String
trim = reverse . dropWhile isSpace . reverse . dropWhile isSpace

stripQuotes :: String -> String
stripQuotes ('"' : rest) = case reverse rest of
    '"' : inner -> reverse inner
    _ -> rest
stripQuotes s = s
