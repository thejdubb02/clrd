package me.itsjustin.clrd

import android.content.Context
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.pm.ResolveInfo
import android.os.PowerManager
import android.provider.Settings
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.After
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.android.controller.ActivityController
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class BatteryPromptTest {
    private val ctx = ApplicationProvider.getApplicationContext<Context>()
    private val power = shadowOf(ctx.getSystemService(PowerManager::class.java))
    private val screens = mutableListOf<ActivityController<MainActivity>>()

    // Never made visible, so its screen is never composed: a composed screen here left Compose stuck in the shared
    // test JVM, and Compose tests later in the run never went idle (#161). These tests only need the activity itself.
    private fun open() = Robolectric.buildActivity(MainActivity::class.java).create().start().resume().also { screens += it }

    @After fun close() = screens.forEach { it.pause().stop().destroy() }

    @Test fun asksWhileOptimised() {
        power.setIgnoringBatteryOptimizations(ctx.packageName, false)
        val i = batteryPrompt(ctx)!!
        assertEquals(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, i.action)
        assertEquals("package:${ctx.packageName}", i.dataString) // the system dialog for this app, not the list of all apps
    }

    @Test fun silentOnceAllowed() {
        power.setIgnoringBatteryOptimizations(ctx.packageName, true)
        assertNull(batteryPrompt(ctx))
    }

    @Test fun aChangeMadeInSettingsShowsOnReturn() {
        power.setIgnoringBatteryOptimizations(ctx.packageName, false)
        val screen = open()
        assertNotNull(screen.get().battery.value)
        power.setIgnoringBatteryOptimizations(ctx.packageName, true) // allowed in Android's settings
        screen.pause().resume()
        assertNull(screen.get().battery.value)
        power.setIgnoringBatteryOptimizations(ctx.packageName, false) // and taken back
        screen.pause().resume()
        assertNotNull(screen.get().battery.value)
    }

    @Test fun noDialogOpensTheListOfApps() {
        val a = open().get()
        shadowOf(a.application).checkActivities(true) // an intent nothing handles throws, as on a real phone
        val list = Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)
        shadowOf(a.packageManager).addResolveInfoForIntent(list, ResolveInfo().apply {
            activityInfo = ActivityInfo().apply { packageName = "com.android.settings"; name = "List" }
        })
        power.setIgnoringBatteryOptimizations(ctx.packageName, false)
        openBattery(a, batteryPrompt(a)!!)
        assertEquals(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS, shadowOf(a).nextStartedActivity?.action)
    }
}
