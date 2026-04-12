-- | Source code positions and regions.
-- Adapted from Elm's Reporting.Annotation.
module Sky.Reporting.Annotation where


-- | A position in source code (line + column, 1-based)
data Position = Position
    { _line   :: {-# UNPACK #-} !Int
    , _col    :: {-# UNPACK #-} !Int
    }
    deriving (Eq, Ord, Show)


-- | A region (span) in source code
data Region = Region
    { _start :: !Position
    , _end   :: !Position
    }
    deriving (Eq, Ord, Show)


-- | A value with its source location
data Located a = At !Region a
    deriving (Eq, Ord, Show)


-- | Extract the value from a Located
toValue :: Located a -> a
toValue (At _ a) = a


-- | Extract the region from a Located
toRegion :: Located a -> Region
toRegion (At r _) = r


-- | Map over the value inside a Located
map :: (a -> b) -> Located a -> Located b
map f (At r a) = At r (f a)


-- | Merge two regions (smallest region containing both)
merge :: Region -> Region -> Region
merge (Region s1 _) (Region _ e2) = Region s1 e2


-- | Zero position (for synthetic nodes)
zero :: Position
zero = Position 1 1


-- | Region at the start of the file
one :: Region
one = Region zero zero
