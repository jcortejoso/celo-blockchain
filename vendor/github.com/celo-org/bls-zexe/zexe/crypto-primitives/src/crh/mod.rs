use algebra::bytes::ToBytes;
use rand::Rng;
use std::hash::Hash;

pub mod injective_map;
pub mod pedersen;
pub mod bowe_hopwood;

use crate::Error;


#[cfg(feature = "r1cs")]
pub mod constraints;
#[cfg(feature = "r1cs")]
pub use constraints::*;


pub trait FixedLengthCRH {
    const INPUT_SIZE_BITS: usize;
    type Output: ToBytes + Clone + Eq + Hash + Default;
    type Parameters: Clone + Default;

    fn setup<R: Rng>(r: &mut R) -> Result<Self::Parameters, Error>;
    fn evaluate(parameters: &Self::Parameters, input: &[u8]) -> Result<Self::Output, Error>;
}
