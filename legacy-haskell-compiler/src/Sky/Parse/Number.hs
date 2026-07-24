-- | Numeric literal parsing for Sky.
-- Handles: integers, floats, hex (0x...), negative literals
module Sky.Parse.Number where

import Data.Char (isDigit, digitToInt, isHexDigit)
import qualified Data.Text as T
import Sky.Parse.Primitives


-- | A parsed number
data Number
    = IntNum !Int
    | FloatNum !Double
    deriving (Show)


-- | Parse a numeric literal
number :: (Row -> Col -> x) -> Parser x Number
number mkError = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just ('0', rest1)
            | Just ('x', rest2) <- T.uncons rest1 ->
                -- Hex literal: 0xFF
                let (hexDigits, rest3) = T.span isHexDigit rest2
                in if T.null hexDigits
                    then eerr (_row s) (_col s) mkError
                    else
                        let val = T.foldl' (\acc c -> acc * 16 + fromIntegral (digitToInt c)) 0 hexDigits
                            len = 2 + T.length hexDigits  -- "0x" + digits
                        in cok (IntNum val) (s { _src = rest3, _offset = _offset s + len, _col = _col s + len })

        Just (c, _)
            | isDigit c ->
                let (digits, rest1) = T.span isDigit (_src s)
                in case T.uncons rest1 of
                    Just ('.', rest2)
                        | Just (d, _) <- T.uncons rest2, isDigit d ->
                            -- Float: 123.456 (optionally with exponent: 1.5e-2 / 2.0E+10).
                            let (decimals, rest3) = T.span isDigit rest2
                                mantissa = T.unpack digits ++ "." ++ T.unpack decimals
                                mantissaLen = T.length digits + 1 + T.length decimals
                                (expPart, rest4, expLen) = parseExponent rest3
                                floatStr = mantissa ++ expPart
                                totalLen = mantissaLen + expLen
                            in cok (FloatNum (read floatStr)) (s { _src = rest4, _offset = _offset s + totalLen, _col = _col s + totalLen })
                    -- Integer with exponent (1e6) becomes a Float.
                    _ | (expPart, rest2, expLen) <- parseExponent rest1
                      , expLen > 0 ->
                          let floatStr = T.unpack digits ++ expPart
                              totalLen = T.length digits + expLen
                          in cok (FloatNum (read floatStr)) (s { _src = rest2, _offset = _offset s + totalLen, _col = _col s + totalLen })
                    _ ->
                        -- Integer: 123
                        let val = T.foldl' (\acc d -> acc * 10 + digitToInt d) 0 digits
                            len = T.length digits
                        in cok (IntNum val) (s { _src = rest1, _offset = _offset s + len, _col = _col s + len })

        _ -> eerr (_row s) (_col s) mkError
  where
    -- Scientific-notation exponent: `e` or `E`, optional `+`/`-`, one or
    -- more digits. Returns the exponent chunk (prefixed with `e`) so the
    -- caller can concatenate it with the mantissa; empty string + zero
    -- length when no exponent is present.
    parseExponent txt = case T.uncons txt of
        Just (e, afterE) | e == 'e' || e == 'E' ->
            let (signStr, afterSign, signLen) = case T.uncons afterE of
                    Just (c, rest) | c == '+' || c == '-' -> ([c], rest, 1)
                    _                                     -> ([],  afterE, 0)
                (expDigits, afterDigits) = T.span isDigit afterSign
            in if T.null expDigits
                then ("", txt, 0)
                else ( [e] ++ signStr ++ T.unpack expDigits
                     , afterDigits
                     , 1 + signLen + T.length expDigits )
        _ -> ("", txt, 0)
