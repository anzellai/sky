-- | Sky keyword recognition.
-- Sky keywords: if, then, else, case, of, let, in, type, module, import,
-- exposing, as, where (reserved), port (reserved), foreign
module Sky.Parse.Keyword where

import qualified Data.Set as Set


-- | The set of Sky keywords
keywords :: Set.Set String
keywords = Set.fromList
    [ "if", "then", "else"
    , "case", "of"
    , "let", "in"
    , "type", "alias"
    , "module", "import", "exposing", "as"
    , "foreign"
    , "True", "False"
    ]


-- | Check if a string is a keyword
isKeyword :: String -> Bool
isKeyword = flip Set.member keywords
