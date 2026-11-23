fn main() {
    // Naming the commands makes each one need its own permission, which only
    // capabilities/device.json grants, and only to the bundled page.
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "state",
            "start",
            "cancel",
            "unlink",
            "retry",
            "set_close_clears",
        ]),
    ))
    .expect("tauri build");
}
