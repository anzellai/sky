{-# LANGUAGE TemplateHaskell #-}
-- | Scrape every kernel-registry entry from
-- `src/Sky/Type/Constrain/Expression.hs` at TH compile time.
-- The actual parser lives in `Sky.Build.KernelRegistryParser`
-- (TH stage restriction — splices can only call code from
-- imported modules).
module Sky.Build.KernelRegistryEntries
    ( kernelEntries
    ) where

import           Language.Haskell.TH
import           Language.Haskell.TH.Syntax (qAddDependentFile, runIO)
import           Sky.Build.KernelRegistryParser (parseEntries)


kernelEntries :: [(String, String)]
kernelEntries = $(do
    let path = "src/Sky/Type/Constrain/Expression.hs"
    qAddDependentFile path
    src <- runIO (readFile path)
    let entries = parseEntries src
    return (ListE [ TupE [Just (LitE (StringL m)), Just (LitE (StringL n))]
                  | (m, n) <- entries
                  ])
  )
