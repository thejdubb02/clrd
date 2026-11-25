package me.itsjustin.clrd

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** #643: this phone unlinks itself by "this", so a publish-only phone can too; others by id. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class UnlinkPathTest {
    @Test fun thisPhoneUsesThis() =
        assertEquals("/api/v1/devices/this", unlinkPath(JSONObject().put("id", "tk_1").put("this", true)))

    @Test fun anotherDeviceUsesItsId() =
        assertEquals("/api/v1/devices/tk%2F2", unlinkPath(JSONObject().put("id", "tk/2")))
}
