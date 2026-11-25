package me.itsjustin.clrd

import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.test.core.app.ApplicationProvider
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
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

    @After fun tearDown() = app.pickAppearance("system")

    @Test fun darkPickAppliesAtOnceAndIsKept() {
        rule.setContent {
            val look by app.appearance.collectAsState()
            ClrdTheme(look) { brand = LocalBrand.current; SettingsScreen(app, null, 0) {} }
        }
        assertSame(LightBrand, brand) // System on a light phone
        rule.onNodeWithTag("settings-search").performTextReplacement("dark")
        rule.onNodeWithText("Appearance").assertExists()
        rule.onNodeWithText("Dark").performScrollTo().performClick()
        rule.waitForIdle()
        assertSame(DarkBrand, brand)
        assertEquals("dark", app.prefs.getString("appearance", null))
        rule.onNodeWithText("Light").performClick()
        rule.waitForIdle()
        assertSame(LightBrand, brand)
    }

    @Test fun systemFollowsTheDevice() {
        rule.setContent { ClrdTheme("system") { brand = LocalBrand.current } }
        assertSame(LightBrand, brand)
    }

    @Test @Config(qualifiers = "night") fun systemFollowsADarkDevice() {
        rule.setContent { ClrdTheme("system") { brand = LocalBrand.current } }
        assertSame(DarkBrand, brand)
    }
}
