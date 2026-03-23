#[cfg(feature = "service")]
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    utils_rust::service_impl::run_server().await
}

#[cfg(not(feature = "service"))]
fn main() {}
