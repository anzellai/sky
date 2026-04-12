-- | Operator and symbol parsing for Sky.
module Sky.Parse.Symbol where

import Data.Char (isSymbol, isPunctuation)
import qualified Data.Text as T
import qualified Data.Set as Set
import Sky.Parse.Primitives


-- | Parse an operator symbol
operator :: (Row -> Col -> x) -> Parser x String
operator mkError = Parser $ \s cok _eok _cerr eerr ->
    let (sym, rest) = T.span isOperatorChar (_src s)
    in if T.null sym
        then eerr (_row s) (_col s) mkError
        else
            let symStr = T.unpack sym
                len = T.length sym
            in cok symStr (s { _src = rest, _offset = _offset s + len, _col = _col s + len })


-- | Characters that can appear in operators
isOperatorChar :: Char -> Bool
isOperatorChar c = c `Set.member` operatorChars


operatorChars :: Set.Set Char
operatorChars = Set.fromList "+-*/<>=!&|^~%?@#$:.\\'"


-- | Known Sky operators (for precedence)
data Precedence = Precedence !Int !Assoc

data Assoc = L | R | N
    deriving (Eq, Show)


-- | Get the precedence and associativity of an operator
precedence :: String -> Precedence
precedence op = case op of
    ">>"  -> Precedence 9 L
    "<<"  -> Precedence 9 R
    "^"   -> Precedence 8 R
    "*"   -> Precedence 7 L
    "/"   -> Precedence 7 L
    "//"  -> Precedence 7 L
    "%"   -> Precedence 7 L
    "+"   -> Precedence 6 L
    "-"   -> Precedence 6 L
    "++"  -> Precedence 5 R
    "::"  -> Precedence 5 R
    "=="  -> Precedence 4 N
    "/="  -> Precedence 4 N
    "<"   -> Precedence 4 N
    ">"   -> Precedence 4 N
    "<="  -> Precedence 4 N
    ">="  -> Precedence 4 N
    "&&"  -> Precedence 3 R
    "||"  -> Precedence 2 R
    "|>"  -> Precedence 0 L
    "<|"  -> Precedence 0 R
    _     -> Precedence 9 L  -- default
