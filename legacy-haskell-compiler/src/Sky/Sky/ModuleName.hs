-- | Sky module names. Unlike Elm, Sky module names have NO package prefix.
-- A module is simply identified by its dotted name: "Sky.Core.List", "Main", "Lib.Auth".
module Sky.Sky.ModuleName where

import qualified Data.Map.Strict as Map


-- | A raw module name as a list of segments.
-- "Sky.Core.List" = ["Sky", "Core", "List"]
type Raw = [String]


-- | A canonical (fully resolved) module name.
-- No package prefix — just the module name.
newtype Canonical = Canonical { _name :: String }
    deriving (Eq, Ord, Show)


-- | Create a Canonical from a raw name
fromRaw :: Raw -> Canonical
fromRaw parts = Canonical (joinWith "." parts)


-- | Convert to display string
toString :: Canonical -> String
toString (Canonical n) = n


-- | Check if a module is from Sky.Core.*
isSkyCore :: Canonical -> Bool
isSkyCore (Canonical n) =
    take 9 n == "Sky.Core."


-- | Check if a module is from Std.*
isStd :: Canonical -> Bool
isStd (Canonical n) =
    take 4 n == "Std."


-- | Check if a module is a standard library module
isStdlib :: Canonical -> Bool
isStdlib m = isSkyCore m || isStd m


-- | Built-in module names
basics, list, string, maybe_, result_, dict, set, task, cmd, sub, html, attr :: Canonical
basics   = Canonical "Sky.Core.Basics"
list     = Canonical "Sky.Core.List"
string   = Canonical "Sky.Core.String"
maybe_   = Canonical "Sky.Core.Maybe"
result_  = Canonical "Sky.Core.Result"
dict     = Canonical "Sky.Core.Dict"
set      = Canonical "Sky.Core.Set"
task     = Canonical "Sky.Core.Task"
cmd      = Canonical "Std.Cmd"
sub      = Canonical "Std.Sub"
html     = Canonical "Sky.Core.Html"
attr     = Canonical "Sky.Core.Html"


-- Utilities

joinWith :: String -> [String] -> String
joinWith _ []     = ""
joinWith _ [x]    = x
joinWith sep (x:xs) = x ++ sep ++ joinWith sep xs
