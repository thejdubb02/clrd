package me.itsjustin.clrd

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.luminance
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** #313: a dark icon (GitHub's) stays visible in the dark theme; the light theme is unchanged. */
class IconTileTest {
    @Test fun darkThemePutsIconsOnALightTile() {
        assertTrue(DarkBrand.iconTile.luminance() > 0.8f)
        assertTrue(DarkBrand.iconTile.luminance() > DarkBrand.paper.luminance())
    }

    @Test fun lightThemeHasNoTile() = assertEquals(Color.Transparent, LightBrand.iconTile)
}
