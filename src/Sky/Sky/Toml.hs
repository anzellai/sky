-- | Minimal sky.toml parser for Sky project configuration.
-- No external TOML library dependency — hand-written for simplicity.
module Sky.Sky.Toml where

import qualified Data.Map.Strict as Map
import Data.Char (isSpace)
import Data.List (isPrefixOf, stripPrefix)


-- | Sky project configuration
data SkyConfig = SkyConfig
    { _name        :: !String           -- project name
    , _version     :: !String           -- semver
    , _entry       :: !String           -- entry file (src/Main.sky)
    , _sourceRoot  :: !String           -- source root (src)
    , _binName     :: !String           -- output binary name (app)
    , _goDeps      :: [(String, String)]-- Go dependencies [(pkg, version)]
    , _livePort    :: !Int              -- Sky.Live port (8000)
    , _liveStore   :: !String           -- session store (memory/sqlite/redis)
    , _dbDriver    :: !String           -- database driver (sqlite/postgres)
    , _dbPath      :: !String           -- database path
    }
    deriving (Show)


-- | Default configuration
defaultConfig :: SkyConfig
defaultConfig = SkyConfig
    { _name       = "sky-project"
    , _version    = "0.1.0"
    , _entry      = "src/Main.sky"
    , _sourceRoot = "src"
    , _binName    = "app"
    , _goDeps     = []
    , _livePort   = 8000
    , _liveStore  = "memory"
    , _dbDriver   = ""
    , _dbPath     = ""
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
    _ -> case key of
        "name"    -> config { _name = value }
        "version" -> config { _version = value }
        "entry"   -> config { _entry = value }
        "root"    -> config { _sourceRoot = value }
        "bin"     -> config { _binName = value }
        "port"    -> config { _livePort = read value }
        "store"   -> config { _liveStore = value }
        "driver"  -> config { _dbDriver = value }
        "path"    -> config { _dbPath = value }
        _         -> config


-- Helpers

trim :: String -> String
trim = reverse . dropWhile isSpace . reverse . dropWhile isSpace

stripQuotes :: String -> String
stripQuotes ('"' : rest) = case reverse rest of
    '"' : inner -> reverse inner
    _ -> rest
stripQuotes s = s
