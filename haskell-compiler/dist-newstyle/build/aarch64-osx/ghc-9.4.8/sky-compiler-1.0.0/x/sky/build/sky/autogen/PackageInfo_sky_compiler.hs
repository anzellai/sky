{-# LANGUAGE NoRebindableSyntax #-}
{-# OPTIONS_GHC -fno-warn-missing-import-lists #-}
{-# OPTIONS_GHC -w #-}
module PackageInfo_sky_compiler (
    name,
    version,
    synopsis,
    copyright,
    homepage,
  ) where

import Data.Version (Version(..))
import Prelude

name :: String
name = "sky_compiler"
version :: Version
version = Version [1,0,0] []

synopsis :: String
synopsis = "Sky language compiler \8212 Elm-inspired, compiles to typed Go"
copyright :: String
copyright = ""
homepage :: String
homepage = ""
