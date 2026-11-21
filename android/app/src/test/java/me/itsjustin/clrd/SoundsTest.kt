package me.itsjustin.clrd

import android.Manifest
import android.app.NotificationManager
import android.net.Uri
import android.provider.Settings
import androidx.test.core.app.ApplicationProvider
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

/** Notification sounds (#282): the choice really changes the channel a notification posts on, and what it plays. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SoundsTest {
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val nm = app.getSystemService(NotificationManager::class.java)

    @Before fun setUp() {
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        app.serverUrl = "https://a.example"
        app.token = "" // not configured, so nothing here touches the network
    }

    @After fun tearDown() { bg { app.db.dao().forget(); app.saveSound(Sounds.DEFAULT) } }

    private fun row(id: String, priority: Int = 3) =
        Notif(id, "disk", "T", "m", priority, "", null, "[]", Notif.ACTIVE, null, null, 1, System.currentTimeMillis(), 1)

    @Test fun theSixSoundsShipInTheApp() {
        for (name in Sounds.BUILT_IN) assertNotEquals(name, 0, app.resources.getIdentifier(name, "raw", app.packageName))
    }

    @Test fun unsetMeansThePhoneDefaultOnTheOldChannels() {
        assertEquals(Sounds.DEFAULT, app.sound)
        assertEquals("p3", Sounds.channel(3, app.sound))
        assertEquals("p3", Sounds.channel(3, "trumpet")) // an unknown name plays default
        app.tray.apply(TrayStep.Show(row("a"), silent = false))
        assertEquals("p3", shadowOf(nm).getNotification("a", 0).channelId)
    }

    @Test fun pickingASoundMovesPostsToItsChannelAndDropsTheOldOnes() {
        bg { app.saveSound("chime") }
        bg { app.db.dao().applyAll(listOf(row("a", priority = 4)), 1) } // in Room, so the switch below reposts it
        app.tray.apply(TrayStep.Show(row("a", priority = 4), silent = false))
        assertEquals("p4-chime", shadowOf(nm).getNotification("a", 0).channelId)
        assertEquals(Uri.parse("android.resource://${app.packageName}/raw/chime"), nm.getNotificationChannel("p4-chime").sound)
        assertNull("the default channels are gone", nm.getNotificationChannel("p4"))
        assertTrue("the sync notice keeps its channel", nm.getNotificationChannel(Tray.SERVICE_CHANNEL) != null)

        bg { app.saveSound("none") }
        assertNull(nm.getNotificationChannel("p4-none").sound)
        assertNull(nm.getNotificationChannel("p4-chime"))
        assertEquals("a showing row moves to the new channel", "p4-none", shadowOf(nm).getNotification("a", 0).channelId)
    }

    @Test fun aChannelTheUserBlockedStaysBlockedOnTheNewSound() {
        nm.deleteNotificationChannel("p5")
        nm.createNotificationChannel(android.app.NotificationChannel("p5", "Urgent", NotificationManager.IMPORTANCE_NONE))
        bg { app.saveSound("chime") }
        assertEquals(NotificationManager.IMPORTANCE_NONE, nm.getNotificationChannel("p5-chime").importance)
        assertEquals("p4 untouched", NotificationManager.IMPORTANCE_HIGH, nm.getNotificationChannel("p4-chime").importance)
    }

    @Test fun aSilentPostStaysSilentWhateverTheSound() {
        bg { app.saveSound("alert") }
        app.tray.apply(TrayStep.Show(row("a", priority = 5), silent = true))
        val n = shadowOf(nm).getNotification("a", 0)
        assertEquals("p5-alert", n.channelId)
        // How NotificationCompat silences one post on API 26+: only a group summary may alert, and this is not one.
        assertEquals(android.app.Notification.GROUP_ALERT_SUMMARY, n.groupAlertBehavior)
    }

    @Test fun aSoundChangeFromTheServerThatDropsTheDndPassSaysSoInTheTray() { // #380
        app.tray.createChannels(Sounds.DEFAULT)
        nm.getNotificationChannel("p5").setBypassDnd(true) // the person let urgent ones through
        bg { app.saveSettings(org.json.JSONObject("""{"sound":"chime"}""")) }
        val n = shadowOf(nm).getNotification(null, Tray.DND_ID)
        assertTrue("the tray says so", n != null)
        val open = shadowOf(n.contentIntent).savedIntent
        assertEquals(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS, open.action)
        assertEquals("p5-chime", open.getStringExtra(Settings.EXTRA_CHANNEL_ID))

        nm.getNotificationChannel("p5-chime").setBypassDnd(true) // turned back on
        assertFalse(app.tray.dndLost())
        assertNull("the notice goes once the pass is back", shadowOf(nm).getNotification(null, Tray.DND_ID))

        bg { app.saveSettings(org.json.JSONObject("""{"sound":"ding"}""")) }
        assertTrue(app.tray.dndLost()) // dropped again, so a new notice
        shadowOf(nm).cancelAll()
        bg { app.saveSettings(org.json.JSONObject("""{"sound":"alert"}""")) }
        assertNull("no pass to lose, no notice", shadowOf(nm).getNotification(null, Tray.DND_ID))
        app.prefs.edit().remove("dnd_lost").commit()
    }

    @Test fun theAppSaysWhenThePhoneSettingsWin() {
        val chime = Uri.parse("android.resource://me.itsjustin.clrd/raw/chime")
        val ok = (3..5).associateWith { chime to NotificationManager.IMPORTANCE_HIGH }
        assertFalse(Sounds.overridden(chime, ok))
        assertTrue("sound changed in system settings", Sounds.overridden(chime, ok + (4 to (Settings.System.DEFAULT_NOTIFICATION_URI to NotificationManager.IMPORTANCE_HIGH))))
        assertTrue("turned down to silent", Sounds.overridden(chime, ok + (5 to (chime to NotificationManager.IMPORTANCE_LOW))))
        assertFalse("a missing channel is not an override", Sounds.overridden(chime, ok + (3 to null)))
        bg { app.saveSound("ding") }
        assertFalse(app.tray.soundOverridden())
    }

    /** #346: Android lets only the person set the Do Not Disturb pass, so a new sound drops it and Settings says so. */
    @Test fun aSoundChangeThatDropsTheDndPassSaysSo() {
        assertTrue(Sounds.dndLost(was = false, replacedHadIt = true, nowHasIt = false))
        assertFalse("never had the pass", Sounds.dndLost(was = false, replacedHadIt = false, nowHasIt = false))
        assertTrue("a later start on the same sound keeps the notice", Sounds.dndLost(was = true, replacedHadIt = false, nowHasIt = false))
        assertFalse("set again, so the notice goes", Sounds.dndLost(was = true, replacedHadIt = false, nowHasIt = true))

        app.tray.createChannels() // a start on the same sound replaces nothing
        assertFalse(app.tray.dndLost())
        // The person let urgent ones through on the default sound. Faked on Robolectric's stored channel,
        // after the start above, because Robolectric keeps no bypass from a create.
        nm.getNotificationChannel("p5").setBypassDnd(true)
        assertTrue(nm.getNotificationChannel("p5").canBypassDnd())
        assertFalse(app.tray.dndLost())
        bg { app.saveSound("chime") }
        assertTrue(app.tray.dndLost())
        app.tray.createChannels()
        assertTrue("still lost after a restart", app.tray.dndLost())
        app.prefs.edit().remove("dnd_lost").commit()
    }

    /** The once-only move of a sound picked before the server kept it (#282, tested for #346). */
    @Test fun anOldSoundGoesToTheServerOnce() {
        app.prefs.edit().remove("sound_moved").remove("sound_pending").putString("sound", "bell").commit()
        app.moveSound()
        assertEquals("bell", app.soundPending)
        assertTrue(app.prefs.getBoolean("sound_moved", false))
        app.prefs.edit().remove("sound_pending").putString("sound", "ding").commit()
        app.moveSound()
        assertNull("only once", app.soundPending)
        app.prefs.edit().remove("sound_moved").putString("sound", Sounds.DEFAULT).commit()
        app.moveSound()
        assertNull("the default has nothing to send", app.soundPending)
        assertTrue(app.prefs.getBoolean("sound_moved", false))
    }
}
