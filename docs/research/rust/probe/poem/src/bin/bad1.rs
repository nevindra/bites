use poem_openapi::{param::Path, payload::PlainText, OpenApi};
struct Api;
#[OpenApi]
impl Api {
  // path names {nope}? poem uses :name ; param is id
  #[oai(path="/u/:nope", method="get")]
  async fn one(&self, id: Path<u32>) -> PlainText<String> { PlainText(format!("{}", id.0)) }
}
fn main(){ let _ = poem_openapi::OpenApiService::new(Api, "t", "1"); }
