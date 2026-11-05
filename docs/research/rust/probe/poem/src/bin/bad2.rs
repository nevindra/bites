use poem_openapi::{payload::PlainText, OpenApi};
struct NotExtractor;
struct Api;
#[OpenApi]
impl Api {
  #[oai(path="/u", method="get")]
  async fn one(&self, x: NotExtractor) -> PlainText<String> { PlainText("".into()) }
}
fn main(){}
