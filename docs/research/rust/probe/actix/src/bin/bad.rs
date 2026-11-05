use actix_web::{web, App};
struct NotExtractor;
async fn bad(_x: NotExtractor) {}
fn main(){ let _ = App::new().route("/", web::get().to(bad)); }
