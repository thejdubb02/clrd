package me.itsjustin.clrd

import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.platform.LocalContext
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp

/** Brand tokens from docs/SPEC.md section 10. Dark mode swaps paper and ink; accents stay. */
data class Brand(
    val ink: Color,
    val paper: Color,
    val surface: Color,
    val line: Color,
    val slate: Color,
    val body: Color,
    val clear: Color,
    val signal: Color = Color(0xFFC2410C),
    val snooze: Color = Color(0xFFB7791F),
    /** Behind a notification's icon. Icons are drawn for light backgrounds, so dark ones (GitHub's) need a light tile in dark mode (#313). */
    val iconTile: Color = Color.Transparent,
)

val LightBrand = Brand(
    ink = Color(0xFF0F1114), paper = Color(0xFFF5F3EE), surface = Color.White, line = Color(0xFFDEDAD1),
    slate = Color(0xFF5B6470), body = Color(0xFF3D444D), clear = Color(0xFF0B7A55),
)

// Dark surface, line and text greys are not in the spec yet; picked to keep the same contrast steps.
val DarkBrand = Brand(
    ink = Color(0xFFF5F3EE), paper = Color(0xFF0F1114), surface = Color(0xFF181B20), line = Color(0xFF2C3036),
    slate = Color(0xFF98A1AC), body = Color(0xFFC9CDD3), clear = Color(0xFF3DDC97), signal = Color(0xFFF97316), // slate, clear and signal: SPEC section 10 (#583)
    iconTile = Color(0xFFF5F3EE), // paper, as on the web inbox
)

/** State chip colours (bg, text). */
fun chip(state: String): Pair<Color, Color> = when (state) {
    Notif.ACTIVE -> Color(0xFFFCE9DF) to Color(0xFF9A3412)
    Notif.ACKNOWLEDGED -> Color(0xFFE9E6DE) to Color(0xFF3D444D)
    Notif.RESOLVED -> Color(0xFFDDF1E8) to Color(0xFF065F42)
    Notif.SNOOZED -> Color(0xFFF7EBD3) to Color(0xFF7A4D0B)
    else -> Color.Transparent to Color(0xFF5B6470) // dismissed: outline only
}

val Geist = FontFamily(Font(R.font.geist_regular, FontWeight.Normal), Font(R.font.geist_semibold, FontWeight.SemiBold))
val GeistMono = FontFamily(Font(R.font.geist_mono))

val LocalBrand = staticCompositionLocalOf { LightBrand }

val Mono = TextStyle(fontFamily = GeistMono, fontSize = 12.sp)
val Wordmark = TextStyle(fontFamily = Geist, fontWeight = FontWeight.SemiBold, fontSize = 28.sp, letterSpacing = (-0.05).em)

@Composable
fun ClrdTheme(appearance: String = "system", content: @Composable () -> Unit) {
    val dark = when (appearance) { "light" -> false; "dark" -> true; else -> isSystemInDarkTheme() }
    // The status and navigation bar icons follow the pick too, not the phone's own mode. Scrims are the library's defaults.
    val act = LocalContext.current as? ComponentActivity
    LaunchedEffect(act, dark) {
        act?.enableEdgeToEdge(
            SystemBarStyle.auto(0, 0) { dark },
            SystemBarStyle.auto(0xE6FFFFFF.toInt(), 0x801B1B1B.toInt()) { dark },
        )
    }
    val b = if (dark) DarkBrand else LightBrand
    val scheme = (if (dark) darkColorScheme() else lightColorScheme()).copy(
        primary = b.clear, onPrimary = b.paper, background = b.paper, onBackground = b.ink,
        surface = b.paper, onSurface = b.ink, surfaceVariant = b.surface, onSurfaceVariant = b.slate,
        outline = b.line, secondaryContainer = b.line, onSecondaryContainer = b.ink,
        surfaceContainer = b.paper, inverseSurface = b.ink, inverseOnSurface = b.paper, inversePrimary = b.clear,
    )
    val t = Typography()
    val type = Typography(
        bodyLarge = t.bodyLarge.copy(fontFamily = Geist),
        bodyMedium = t.bodyMedium.copy(fontFamily = Geist),
        bodySmall = t.bodySmall.copy(fontFamily = Geist),
        labelLarge = t.labelLarge.copy(fontFamily = Geist),
        labelMedium = t.labelMedium.copy(fontFamily = Geist),
        labelSmall = t.labelSmall.copy(fontFamily = Geist),
        titleMedium = t.titleMedium.copy(fontFamily = Geist),
        titleLarge = t.titleLarge.copy(fontFamily = Geist),
    )
    CompositionLocalProvider(LocalBrand provides b) {
        MaterialTheme(colorScheme = scheme, typography = type, content = content)
    }
}
