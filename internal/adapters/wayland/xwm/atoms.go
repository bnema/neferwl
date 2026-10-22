package xwm

// Atoms interned at boot, in this fixed order. Clipboard and selection atoms
// are interned now so later phases add no round trip.
const (
	atomWMProtocols = iota
	atomWMDeleteWindow
	atomWMTakeFocus
	atomWMState
	atomWMChangeState
	atomWMS0
	atomUTF8String
	atomNetWMCMS0
	atomNetSupported
	atomNetSupportingWMCheck
	atomNetWMName
	atomNetWMPID
	atomNetWMWindowType
	atomNetWMWindowTypeNormal
	atomNetWMWindowTypeDialog
	atomNetWMWindowTypeUtility
	atomNetWMWindowTypeSplash
	atomNetWMWindowTypeToolbar
	atomNetWMWindowTypeMenu
	atomNetWMWindowTypeDropdownMenu
	atomNetWMWindowTypePopupMenu
	atomNetWMWindowTypeTooltip
	atomNetWMWindowTypeNotification
	atomNetWMWindowTypeCombo
	atomNetWMWindowTypeDND
	atomNetWMState
	atomNetWMStateModal
	atomNetWMStateFullscreen
	atomNetWMStateFocused
	atomNetWMStateMaximizedVert
	atomNetWMStateMaximizedHorz
	atomNetActiveWindow
	atomNetClientList
	atomNetClientListStacking
	atomNetWMMoveresize
	atomNetWMBypassCompositor
	atomWLSurfaceID
	atomWLSurfaceSerial
	atomNetWMIcon
	atomMotifWMHints
	atomClipboard
	atomPrimary
	atomTargets
	atomCount
)

var atomNames = [atomCount]string{
	atomWMProtocols:                 "WM_PROTOCOLS",
	atomWMDeleteWindow:              "WM_DELETE_WINDOW",
	atomWMTakeFocus:                 "WM_TAKE_FOCUS",
	atomWMState:                     "WM_STATE",
	atomWMChangeState:               "WM_CHANGE_STATE",
	atomWMS0:                        "WM_S0",
	atomUTF8String:                  "UTF8_STRING",
	atomNetWMCMS0:                   "_NET_WM_CM_S0",
	atomNetSupported:                "_NET_SUPPORTED",
	atomNetSupportingWMCheck:        "_NET_SUPPORTING_WM_CHECK",
	atomNetWMName:                   "_NET_WM_NAME",
	atomNetWMPID:                    "_NET_WM_PID",
	atomNetWMWindowType:             "_NET_WM_WINDOW_TYPE",
	atomNetWMWindowTypeNormal:       "_NET_WM_WINDOW_TYPE_NORMAL",
	atomNetWMWindowTypeDialog:       "_NET_WM_WINDOW_TYPE_DIALOG",
	atomNetWMWindowTypeUtility:      "_NET_WM_WINDOW_TYPE_UTILITY",
	atomNetWMWindowTypeSplash:       "_NET_WM_WINDOW_TYPE_SPLASH",
	atomNetWMWindowTypeToolbar:      "_NET_WM_WINDOW_TYPE_TOOLBAR",
	atomNetWMWindowTypeMenu:         "_NET_WM_WINDOW_TYPE_MENU",
	atomNetWMWindowTypeDropdownMenu: "_NET_WM_WINDOW_TYPE_DROPDOWN_MENU",
	atomNetWMWindowTypePopupMenu:    "_NET_WM_WINDOW_TYPE_POPUP_MENU",
	atomNetWMWindowTypeTooltip:      "_NET_WM_WINDOW_TYPE_TOOLTIP",
	atomNetWMWindowTypeNotification: "_NET_WM_WINDOW_TYPE_NOTIFICATION",
	atomNetWMWindowTypeCombo:        "_NET_WM_WINDOW_TYPE_COMBO",
	atomNetWMWindowTypeDND:          "_NET_WM_WINDOW_TYPE_DND",
	atomNetWMState:                  "_NET_WM_STATE",
	atomNetWMStateModal:             "_NET_WM_STATE_MODAL",
	atomNetWMStateFullscreen:        "_NET_WM_STATE_FULLSCREEN",
	atomNetWMStateFocused:           "_NET_WM_STATE_FOCUSED",
	atomNetWMStateMaximizedVert:     "_NET_WM_STATE_MAXIMIZED_VERT",
	atomNetWMStateMaximizedHorz:     "_NET_WM_STATE_MAXIMIZED_HORZ",
	atomNetActiveWindow:             "_NET_ACTIVE_WINDOW",
	atomNetClientList:               "_NET_CLIENT_LIST",
	atomNetClientListStacking:       "_NET_CLIENT_LIST_STACKING",
	atomNetWMMoveresize:             "_NET_WM_MOVERESIZE",
	atomNetWMBypassCompositor:       "_NET_WM_BYPASS_COMPOSITOR",
	atomWLSurfaceID:                 "WL_SURFACE_ID",
	atomWLSurfaceSerial:             "WL_SURFACE_SERIAL",
	atomNetWMIcon:                   "_NET_WM_ICON",
	atomMotifWMHints:                "_MOTIF_WM_HINTS",
	atomClipboard:                   "CLIPBOARD",
	atomPrimary:                     "PRIMARY",
	atomTargets:                     "TARGETS",
}
