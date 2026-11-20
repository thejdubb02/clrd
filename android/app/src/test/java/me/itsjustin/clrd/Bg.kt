package me.itsjustin.clrd

/** Room refuses the main thread, which is the Robolectric test thread. */
internal fun <T> bg(f: () -> T): T {
    var r: Result<T>? = null
    Thread { r = runCatching(f) }.apply { start(); join(60_000) } // the first Room open in a JVM is slow on CI
    return checkNotNull(r) { "background step still running after 60 s" }.getOrThrow()
}
