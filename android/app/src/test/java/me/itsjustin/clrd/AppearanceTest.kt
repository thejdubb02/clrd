package me.itsjustin.clrd

import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** Display, Appearance (#583): System by default, Light or Dark per phone, applied at once and kept. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], qualifiers = "notnight")
class AppearanceTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private var brand: Brand? = null

    @Test fun darkPickAppliesAtOnceAndIsKept() {
        rule.setContent {
            val look by app.appearance.collectAsState()
            ClrdTheme(look) { brand = LocalBrand.current; Column { AppearancePicker(app) } }
        }
        assertSame(LightBrand, brand) // System on a light phone
        rule.onNodeWithText("Dark").performClick()
        rule.waitForIdle()
        assertSame(DarkBrand, brand)
        assertEquals("dark", app.prefs.getString("appearance", null))
        rule.onNodeWithText("Light").performClick()
        rule.waitForIdle()
        assertSame(LightBrand, brand)
    }

    @Test fun searchFindsIt() {
        val s = SETTINGS.single { it.id == "appearance" }
        listOf("dark", "theme", "appearance").forEach { assertTrue(it, settingMatches(it, app.getString(s.label), s.desc?.let(app::getString), s.keywords)) }
    }

    @Test fun systemFollowsTheDevice() {
        rule.setContent { ClrdTheme("system") { brand = LocalBrand.current } }
        assertSame(LightBrand, brand)
    }

    @Test @Config(qualifiers = "night") fun systemFollowsADarkDevice() {
        rule.setContent { ClrdTheme("system") { brand = LocalBrand.current } }
        assertSame(DarkBrand, brand)
    }

    @Test fun darkAccentsAreTheSpecValues() { // SPEC section 10, the values the web ships
        fun hex(c: androidx.compose.ui.graphics.Color) = "#%06X".format(c.toArgb() and 0xFFFFFF)
        assertEquals(listOf("#98A1AC", "#3DDC97", "#F97316"), listOf(hex(DarkBrand.slate), hex(DarkBrand.clear), hex(DarkBrand.signal)))
    }
}
