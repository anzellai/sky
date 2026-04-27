-- | Union-Find data structure for type inference.
--
-- Derivative work adapted from elm/compiler's @Type.UnionFind@
-- (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See
-- NOTICE.md at the repo root for the full attribution and licence
-- text.
--
-- Uses weighted union + path compression for near-constant-time
-- operations.
module Sky.Type.UnionFind
    ( Point
    , fresh
    , union
    , equivalent
    , redundant
    , get
    , set
    , modify
    )
    where

import Control.Monad (when)
import Data.IORef (IORef, newIORef, readIORef, writeIORef, modifyIORef')
import Data.Word (Word32)


-- POINT

newtype Point a = Pt (IORef (PointInfo a))


data PointInfo a
    = Info {-# UNPACK #-} !Word32 !(IORef a)
    | Link {-# UNPACK #-} !(Point a)


-- OPERATIONS

{-# INLINE fresh #-}
fresh :: a -> IO (Point a)
fresh value =
    do  valRef <- newIORef value
        Pt <$> newIORef (Info 0 valRef)


-- Find representative with path compression
repr :: Point a -> IO (Point a)
repr point@(Pt ref) =
    do  pInfo <- readIORef ref
        case pInfo of
            Info _ _ ->
                return point

            Link next ->
                do  root <- repr next
                    when (root /= next) $
                        writeIORef ref (Link root)
                    return root


instance Eq (Point a) where
    (Pt ref1) == (Pt ref2) = ref1 == ref2


instance Ord (Point a) where
    compare (Pt ref1) (Pt ref2) = compare (ptrToInt ref1) (ptrToInt ref2)
      where
        ptrToInt :: IORef a -> Int
        ptrToInt = error "UnionFind Ord: not implemented (use Eq)"


-- GET / SET / MODIFY

{-# INLINE get #-}
get :: Point a -> IO a
get point =
    do  (Pt ref) <- repr point
        pInfo <- readIORef ref
        case pInfo of
            Info _ valRef -> readIORef valRef
            Link _        -> error "UnionFind.get: impossible — repr returned a link"


{-# INLINE set #-}
set :: Point a -> a -> IO ()
set point value =
    do  (Pt ref) <- repr point
        pInfo <- readIORef ref
        case pInfo of
            Info _ valRef -> writeIORef valRef value
            Link _        -> error "UnionFind.set: impossible"


{-# INLINE modify #-}
modify :: Point a -> (a -> a) -> IO ()
modify point f =
    do  (Pt ref) <- repr point
        pInfo <- readIORef ref
        case pInfo of
            Info _ valRef -> modifyIORef' valRef f
            Link _        -> error "UnionFind.modify: impossible"


-- UNION

union :: Point a -> Point a -> a -> IO ()
union p1 p2 newValue =
    do  r1@(Pt ref1) <- repr p1
        r2@(Pt ref2) <- repr p2

        when (r1 /= r2) $
            do  info1 <- readIORef ref1
                info2 <- readIORef ref2
                case (info1, info2) of
                    (Info w1 valRef1, Info w2 _) ->
                        if w1 >= w2
                            then do
                                writeIORef valRef1 newValue
                                writeIORef ref2 (Link r1)
                                when (w1 == w2) $
                                    writeIORef ref1 (Info (w1 + 1) valRef1)
                            else do
                                writeIORef ref1 (Link r2)
                                info2' <- readIORef ref2
                                case info2' of
                                    Info _ valRef2 -> writeIORef valRef2 newValue
                                    Link _         -> error "UnionFind.union: impossible"

                    _ -> error "UnionFind.union: repr returned link"


-- EQUIVALENT

{-# INLINE equivalent #-}
equivalent :: Point a -> Point a -> IO Bool
equivalent p1 p2 =
    do  r1 <- repr p1
        r2 <- repr p2
        return (r1 == r2)


-- REDUNDANT

{-# INLINE redundant #-}
redundant :: Point a -> IO Bool
redundant (Pt ref) =
    do  pInfo <- readIORef ref
        case pInfo of
            Info _ _ -> return False
            Link _   -> return True
