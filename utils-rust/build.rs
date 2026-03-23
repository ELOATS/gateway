fn main() -> Result<(), Box<dyn std::error::Error>> {
    #[cfg(feature = "service")]
    tonic_build::compile_protos("../proto/gateway.proto")?;
    Ok(())
}
