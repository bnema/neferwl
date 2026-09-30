package wayland

// Raw-wire test clients intentionally keep raw requests (including invalid ones).
// Their receivers nevertheless obey the real client's context, schema and
// destructor contracts. Metadata comes from generated protocol descriptions;
// request opcode numbers alone never determine object type or destruction.
import (
	"encoding/binary"
	"strings"
	"sync"
	"testing"

	wire_capturesession "github.com/bnema/neferwl/internal/adapters/wayland/capturesession"
	wire_alphamodifier "github.com/bnema/purego-libwayland/protocol/alphamodifier"
	wire_colormanagement "github.com/bnema/purego-libwayland/protocol/colormanagement"
	wire_colorrepresentation "github.com/bnema/purego-libwayland/protocol/colorrepresentation"
	wire_committiming "github.com/bnema/purego-libwayland/protocol/committiming"
	wire_cursorshape "github.com/bnema/purego-libwayland/protocol/cursorshape"
	wire_drmlease "github.com/bnema/purego-libwayland/protocol/drmlease"
	wire_extdatacontrol "github.com/bnema/purego-libwayland/protocol/extdatacontrol"
	wire_extidlenotify "github.com/bnema/purego-libwayland/protocol/extidlenotify"
	wire_extimagecapturesource "github.com/bnema/purego-libwayland/protocol/extimagecapturesource"
	wire_extimagecopycapture "github.com/bnema/purego-libwayland/protocol/extimagecopycapture"
	wire_extsessionlock "github.com/bnema/purego-libwayland/protocol/extsessionlock"
	wire_extworkspace "github.com/bnema/purego-libwayland/protocol/extworkspace"
	wire_fifo "github.com/bnema/purego-libwayland/protocol/fifo"
	wire_fractionalscale "github.com/bnema/purego-libwayland/protocol/fractionalscale"
	wire_idleinhibit "github.com/bnema/purego-libwayland/protocol/idleinhibit"
	wire_inputmethod "github.com/bnema/purego-libwayland/protocol/inputmethod"
	wire_kdedecoration "github.com/bnema/purego-libwayland/protocol/kdedecoration"
	wire_keyboardshortcutsinhibit "github.com/bnema/purego-libwayland/protocol/keyboardshortcutsinhibit"
	wire_linuxdmabuf "github.com/bnema/purego-libwayland/protocol/linuxdmabuf"
	wire_linuxdrmsyncobj "github.com/bnema/purego-libwayland/protocol/linuxdrmsyncobj"
	wire_pointerconstraints "github.com/bnema/purego-libwayland/protocol/pointerconstraints"
	wire_pointerwarp "github.com/bnema/purego-libwayland/protocol/pointerwarp"
	wire_presentationtime "github.com/bnema/purego-libwayland/protocol/presentationtime"
	wire_primaryselection "github.com/bnema/purego-libwayland/protocol/primaryselection"
	wire_relativepointer "github.com/bnema/purego-libwayland/protocol/relativepointer"
	wire_tearingcontrol "github.com/bnema/purego-libwayland/protocol/tearingcontrol"
	wire_textinput "github.com/bnema/purego-libwayland/protocol/textinput"
	wire_viewporter "github.com/bnema/purego-libwayland/protocol/viewporter"
	wire_virtualkeyboard "github.com/bnema/purego-libwayland/protocol/virtualkeyboard"
	wire_wayland "github.com/bnema/purego-libwayland/protocol/wayland"
	wire_wlrforeigntoplevel "github.com/bnema/purego-libwayland/protocol/wlrforeigntoplevel"
	wire_wlrlayershell "github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	wire_wlroutputmanagement "github.com/bnema/purego-libwayland/protocol/wlroutputmanagement"
	wire_wlroutputpower "github.com/bnema/purego-libwayland/protocol/wlroutputpower"
	wire_wlrscreencopy "github.com/bnema/purego-libwayland/protocol/wlrscreencopy"
	wire_xdgactivation "github.com/bnema/purego-libwayland/protocol/xdgactivation"
	wire_xdgoutput "github.com/bnema/purego-libwayland/protocol/xdgoutput"
	wire_xdgshell "github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/wlturbo"
	client_alphamodifier "github.com/bnema/wlturbo/protocol/alphamodifier"
	client_colormanagement "github.com/bnema/wlturbo/protocol/colormanagement"
	client_colorrepresentation "github.com/bnema/wlturbo/protocol/colorrepresentation"
	client_committiming "github.com/bnema/wlturbo/protocol/committiming"
	client_core "github.com/bnema/wlturbo/protocol/core"
	client_cursorshape "github.com/bnema/wlturbo/protocol/cursorshape"
	client_datacontrol "github.com/bnema/wlturbo/protocol/datacontrol"
	client_drmlease "github.com/bnema/wlturbo/protocol/drmlease"
	client_drmsyncobj "github.com/bnema/wlturbo/protocol/drmsyncobj"
	client_extsessionlock "github.com/bnema/wlturbo/protocol/extsessionlock"
	client_fifo "github.com/bnema/wlturbo/protocol/fifo"
	client_foreigntoplevel "github.com/bnema/wlturbo/protocol/foreigntoplevel"
	client_fractionalscale "github.com/bnema/wlturbo/protocol/fractionalscale"
	client_idleinhibit "github.com/bnema/wlturbo/protocol/idleinhibit"
	client_idlenotify "github.com/bnema/wlturbo/protocol/idlenotify"
	client_imagecapturesource "github.com/bnema/wlturbo/protocol/imagecapturesource"
	client_imagecopycapture "github.com/bnema/wlturbo/protocol/imagecopycapture"
	client_inputmethod "github.com/bnema/wlturbo/protocol/inputmethod"
	client_kdeserverdecoration "github.com/bnema/wlturbo/protocol/kdeserverdecoration"
	client_layershell "github.com/bnema/wlturbo/protocol/layershell"
	client_linuxdmabuf "github.com/bnema/wlturbo/protocol/linuxdmabuf"
	client_outputmanagement "github.com/bnema/wlturbo/protocol/outputmanagement"
	client_outputpower "github.com/bnema/wlturbo/protocol/outputpower"
	client_pointerconstraints "github.com/bnema/wlturbo/protocol/pointerconstraints"
	client_pointerwarp "github.com/bnema/wlturbo/protocol/pointerwarp"
	client_presentation "github.com/bnema/wlturbo/protocol/presentation"
	client_primaryselection "github.com/bnema/wlturbo/protocol/primaryselection"
	client_relativepointer "github.com/bnema/wlturbo/protocol/relativepointer"
	client_screencopy "github.com/bnema/wlturbo/protocol/screencopy"
	client_shortcutsinhibit "github.com/bnema/wlturbo/protocol/shortcutsinhibit"
	client_tearingcontrol "github.com/bnema/wlturbo/protocol/tearingcontrol"
	client_textinput "github.com/bnema/wlturbo/protocol/textinput"
	client_viewporter "github.com/bnema/wlturbo/protocol/viewporter"
	client_virtualkeyboard "github.com/bnema/wlturbo/protocol/virtualkeyboard"
	client_workspace "github.com/bnema/wlturbo/protocol/workspace"
	client_xdgactivation "github.com/bnema/wlturbo/protocol/xdgactivation"
	client_xdgoutput "github.com/bnema/wlturbo/protocol/xdgoutput"
	client_xdgshell "github.com/bnema/wlturbo/protocol/xdgshell"
)

type wireSignature interface{ EventSignature(uint16) (string, bool) }
type wireSchema struct {
	iface              *server.Interface
	events             wireSignature
	requestDestructors map[uint16]bool
	eventDestructors   map[uint16]bool
}

var wireSchemas = map[string]wireSchema{
	"ext_data_control_device_v1":                           {wire_extdatacontrol.ExtDataControlDeviceV1Interface, &client_datacontrol.ExtDataControlDevice{}, map[uint16]bool{1: true}, nil},
	"ext_data_control_manager_v1":                          {wire_extdatacontrol.ExtDataControlManagerV1Interface, &client_datacontrol.ExtDataControlManager{}, map[uint16]bool{2: true}, nil},
	"ext_data_control_offer_v1":                            {wire_extdatacontrol.ExtDataControlOfferV1Interface, &client_datacontrol.ExtDataControlOffer{}, map[uint16]bool{1: true}, nil},
	"ext_data_control_source_v1":                           {wire_extdatacontrol.ExtDataControlSourceV1Interface, &client_datacontrol.ExtDataControlSource{}, map[uint16]bool{1: true}, nil},
	"ext_foreign_toplevel_image_capture_source_manager_v1": {wire_extimagecapturesource.ExtForeignToplevelImageCaptureSourceManagerV1Interface, &client_imagecapturesource.ExtForeignToplevelImageCaptureSourceManager{}, map[uint16]bool{1: true}, nil},
	"ext_idle_notification_v1":                             {wire_extidlenotify.ExtIdleNotificationV1Interface, &client_idlenotify.ExtIdleNotification{}, map[uint16]bool{0: true}, nil},
	"ext_idle_notifier_v1":                                 {wire_extidlenotify.ExtIdleNotifierV1Interface, &client_idlenotify.ExtIdleNotifier{}, map[uint16]bool{0: true}, nil},
	"ext_image_capture_source_v1":                          {wire_extimagecapturesource.ExtImageCaptureSourceV1Interface, &client_imagecapturesource.ExtImageCaptureSource{}, map[uint16]bool{0: true}, nil},
	"ext_image_copy_capture_cursor_session_v1":             {wire_extimagecopycapture.ExtImageCopyCaptureCursorSessionV1Interface, &client_imagecopycapture.ExtImageCopyCaptureCursorSession{}, map[uint16]bool{0: true}, nil},
	"ext_image_copy_capture_frame_v1":                      {wire_extimagecopycapture.ExtImageCopyCaptureFrameV1Interface, &client_imagecopycapture.ExtImageCopyCaptureFrame{}, map[uint16]bool{0: true}, nil},
	"ext_image_copy_capture_manager_v1":                    {wire_extimagecopycapture.ExtImageCopyCaptureManagerV1Interface, &client_imagecopycapture.ExtImageCopyCaptureManager{}, map[uint16]bool{2: true}, nil},
	"ext_image_copy_capture_session_v1":                    {wire_extimagecopycapture.ExtImageCopyCaptureSessionV1Interface, &client_imagecopycapture.ExtImageCopyCaptureSession{}, map[uint16]bool{1: true}, nil},
	"ext_output_image_capture_source_manager_v1":           {wire_extimagecapturesource.ExtOutputImageCaptureSourceManagerV1Interface, &client_imagecapturesource.ExtOutputImageCaptureSourceManager{}, map[uint16]bool{1: true}, nil},
	"ext_session_lock_manager_v1":                          {wire_extsessionlock.ExtSessionLockManagerV1Interface, &client_extsessionlock.ExtSessionLockManager{}, map[uint16]bool{0: true}, nil},
	"ext_session_lock_surface_v1":                          {wire_extsessionlock.ExtSessionLockSurfaceV1Interface, &client_extsessionlock.ExtSessionLockSurface{}, map[uint16]bool{0: true}, nil},
	"ext_session_lock_v1":                                  {wire_extsessionlock.ExtSessionLockV1Interface, &client_extsessionlock.ExtSessionLock{}, map[uint16]bool{0: true, 2: true}, nil},
	"ext_workspace_group_handle_v1":                        {wire_extworkspace.ExtWorkspaceGroupHandleV1Interface, &client_workspace.ExtWorkspaceGroupHandle{}, map[uint16]bool{1: true}, nil},
	"ext_workspace_handle_v1":                              {wire_extworkspace.ExtWorkspaceHandleV1Interface, &client_workspace.ExtWorkspaceHandle{}, map[uint16]bool{0: true}, nil},
	"ext_workspace_manager_v1":                             {wire_extworkspace.ExtWorkspaceManagerV1Interface, &client_workspace.ExtWorkspaceManager{}, nil, map[uint16]bool{3: true}},
	"neferwl_capture_layer_v1":                             {wire_capturesession.NeferwlCaptureLayerV1Interface, nil, map[uint16]bool{0: true}, nil},
	"neferwl_capture_manager_v1":                           {wire_capturesession.NeferwlCaptureManagerV1Interface, nil, map[uint16]bool{0: true}, nil},
	"neferwl_capture_session_v1":                           {wire_capturesession.NeferwlCaptureSessionV1Interface, nil, map[uint16]bool{0: true}, nil},
	"org_kde_kwin_server_decoration":                       {wire_kdedecoration.OrgKdeKwinServerDecorationInterface, &client_kdeserverdecoration.OrgKdeKwinServerDecoration{}, map[uint16]bool{0: true}, nil},
	"org_kde_kwin_server_decoration_manager":               {wire_kdedecoration.OrgKdeKwinServerDecorationManagerInterface, &client_kdeserverdecoration.OrgKdeKwinServerDecorationManager{}, nil, nil},
	"wl_buffer":                                            {wire_wayland.BufferInterface, &client_core.Buffer{}, map[uint16]bool{0: true}, nil},
	"wl_callback":                                          {wire_wayland.CallbackInterface, &client_core.Callback{}, nil, map[uint16]bool{0: true}},
	"wl_compositor":                                        {wire_wayland.CompositorInterface, &client_core.Compositor{}, map[uint16]bool{2: true}, nil},
	"wl_data_device":                                       {wire_wayland.DataDeviceInterface, &client_core.DataDevice{}, map[uint16]bool{2: true}, nil},
	"wl_data_device_manager":                               {wire_wayland.DataDeviceManagerInterface, &client_core.DataDeviceManager{}, map[uint16]bool{2: true}, nil},
	"wl_data_offer":                                        {wire_wayland.DataOfferInterface, &client_core.DataOffer{}, map[uint16]bool{2: true}, nil},
	"wl_data_source":                                       {wire_wayland.DataSourceInterface, &client_core.DataSource{}, map[uint16]bool{1: true}, nil},
	"wl_display":                                           {wire_wayland.DisplayInterface, nil, nil, nil},
	"wl_fixes":                                             {wire_wayland.FixesInterface, &client_core.Fixes{}, map[uint16]bool{0: true}, nil},
	"wl_keyboard":                                          {wire_wayland.KeyboardInterface, &client_core.Keyboard{}, map[uint16]bool{0: true}, nil},
	"wl_output":                                            {wire_wayland.OutputInterface, &client_core.Output{}, map[uint16]bool{0: true}, nil},
	"wl_pointer":                                           {wire_wayland.PointerInterface, &client_core.Pointer{}, map[uint16]bool{1: true}, nil},
	"wl_region":                                            {wire_wayland.RegionInterface, &client_core.Region{}, map[uint16]bool{0: true}, nil},
	"wl_registry":                                          {wire_wayland.RegistryInterface, nil, nil, nil},
	"wl_seat":                                              {wire_wayland.SeatInterface, &client_core.Seat{}, map[uint16]bool{3: true}, nil},
	"wl_shell":                                             {wire_wayland.ShellInterface, &client_core.Shell{}, nil, nil},
	"wl_shell_surface":                                     {wire_wayland.ShellSurfaceInterface, &client_core.ShellSurface{}, nil, nil},
	"wl_shm":                                               {wire_wayland.ShmInterface, &client_core.Shm{}, map[uint16]bool{1: true}, nil},
	"wl_shm_pool":                                          {wire_wayland.ShmPoolInterface, &client_core.ShmPool{}, map[uint16]bool{1: true}, nil},
	"wl_subcompositor":                                     {wire_wayland.SubcompositorInterface, &client_core.Subcompositor{}, map[uint16]bool{0: true}, nil},
	"wl_subsurface":                                        {wire_wayland.SubsurfaceInterface, &client_core.Subsurface{}, map[uint16]bool{0: true}, nil},
	"wl_surface":                                           {wire_wayland.SurfaceInterface, &client_core.Surface{}, map[uint16]bool{0: true}, nil},
	"wl_touch":                                             {wire_wayland.TouchInterface, &client_core.Touch{}, map[uint16]bool{0: true}, nil},
	"wp_alpha_modifier_surface_v1":                         {wire_alphamodifier.WpAlphaModifierSurfaceV1Interface, &client_alphamodifier.WpAlphaModifierSurface{}, map[uint16]bool{0: true}, nil},
	"wp_alpha_modifier_v1":                                 {wire_alphamodifier.WpAlphaModifierV1Interface, &client_alphamodifier.WpAlphaModifier{}, map[uint16]bool{0: true}, nil},
	"wp_color_management_output_v1":                        {wire_colormanagement.WpColorManagementOutputV1Interface, &client_colormanagement.WpColorManagementOutput{}, map[uint16]bool{0: true}, nil},
	"wp_color_management_surface_feedback_v1":              {wire_colormanagement.WpColorManagementSurfaceFeedbackV1Interface, &client_colormanagement.WpColorManagementSurfaceFeedback{}, map[uint16]bool{0: true}, nil},
	"wp_color_management_surface_v1":                       {wire_colormanagement.WpColorManagementSurfaceV1Interface, &client_colormanagement.WpColorManagementSurface{}, map[uint16]bool{0: true}, nil},
	"wp_color_manager_v1":                                  {wire_colormanagement.WpColorManagerV1Interface, &client_colormanagement.WpColorManager{}, map[uint16]bool{0: true}, nil},
	"wp_color_representation_manager_v1":                   {wire_colorrepresentation.WpColorRepresentationManagerV1Interface, &client_colorrepresentation.WpColorRepresentationManager{}, map[uint16]bool{0: true}, nil},
	"wp_color_representation_surface_v1":                   {wire_colorrepresentation.WpColorRepresentationSurfaceV1Interface, &client_colorrepresentation.WpColorRepresentationSurface{}, map[uint16]bool{0: true}, nil},
	"wp_commit_timer_v1":                                   {wire_committiming.WpCommitTimerV1Interface, &client_committiming.WpCommitTimer{}, map[uint16]bool{1: true}, nil},
	"wp_commit_timing_manager_v1":                          {wire_committiming.WpCommitTimingManagerV1Interface, &client_committiming.WpCommitTimingManager{}, map[uint16]bool{0: true}, nil},
	"wp_cursor_shape_device_v1":                            {wire_cursorshape.WpCursorShapeDeviceV1Interface, &client_cursorshape.WpCursorShapeDevice{}, map[uint16]bool{0: true}, nil},
	"wp_cursor_shape_manager_v1":                           {wire_cursorshape.WpCursorShapeManagerV1Interface, &client_cursorshape.WpCursorShapeManager{}, map[uint16]bool{0: true}, nil},
	"wp_drm_lease_connector_v1":                            {wire_drmlease.WpDrmLeaseConnectorV1Interface, &client_drmlease.WpDrmLeaseConnector{}, map[uint16]bool{0: true}, nil},
	"wp_drm_lease_device_v1":                               {wire_drmlease.WpDrmLeaseDeviceV1Interface, &client_drmlease.WpDrmLeaseDevice{}, nil, map[uint16]bool{3: true}},
	"wp_drm_lease_request_v1":                              {wire_drmlease.WpDrmLeaseRequestV1Interface, &client_drmlease.WpDrmLeaseRequest{}, map[uint16]bool{1: true}, nil},
	"wp_drm_lease_v1":                                      {wire_drmlease.WpDrmLeaseV1Interface, &client_drmlease.WpDrmLease{}, map[uint16]bool{0: true}, nil},
	"wp_fifo_manager_v1":                                   {wire_fifo.WpFifoManagerV1Interface, &client_fifo.WpFifoManager{}, map[uint16]bool{0: true}, nil},
	"wp_fifo_v1":                                           {wire_fifo.WpFifoV1Interface, &client_fifo.WpFifo{}, map[uint16]bool{2: true}, nil},
	"wp_fractional_scale_manager_v1":                       {wire_fractionalscale.WpFractionalScaleManagerV1Interface, &client_fractionalscale.WpFractionalScaleManager{}, map[uint16]bool{0: true}, nil},
	"wp_fractional_scale_v1":                               {wire_fractionalscale.WpFractionalScaleV1Interface, &client_fractionalscale.WpFractionalScale{}, map[uint16]bool{0: true}, nil},
	"wp_image_description_creator_icc_v1":                  {wire_colormanagement.WpImageDescriptionCreatorIccV1Interface, &client_colormanagement.WpImageDescriptionCreatorIcc{}, map[uint16]bool{0: true}, nil},
	"wp_image_description_creator_params_v1":               {wire_colormanagement.WpImageDescriptionCreatorParamsV1Interface, &client_colormanagement.WpImageDescriptionCreatorParams{}, map[uint16]bool{0: true}, nil},
	"wp_image_description_info_v1":                         {wire_colormanagement.WpImageDescriptionInfoV1Interface, &client_colormanagement.WpImageDescriptionInfo{}, nil, map[uint16]bool{0: true}},
	"wp_image_description_reference_v1":                    {wire_colormanagement.WpImageDescriptionReferenceV1Interface, &client_colormanagement.WpImageDescriptionReference{}, map[uint16]bool{0: true}, nil},
	"wp_image_description_v1":                              {wire_colormanagement.WpImageDescriptionV1Interface, &client_colormanagement.WpImageDescription{}, map[uint16]bool{0: true}, nil},
	"wp_linux_drm_syncobj_manager_v1":                      {wire_linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1Interface, &client_drmsyncobj.WpLinuxDrmSyncobjManager{}, map[uint16]bool{0: true}, nil},
	"wp_linux_drm_syncobj_surface_v1":                      {wire_linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1Interface, &client_drmsyncobj.WpLinuxDrmSyncobjSurface{}, map[uint16]bool{0: true}, nil},
	"wp_linux_drm_syncobj_timeline_v1":                     {wire_linuxdrmsyncobj.WpLinuxDrmSyncobjTimelineV1Interface, &client_drmsyncobj.WpLinuxDrmSyncobjTimeline{}, map[uint16]bool{0: true}, nil},
	"wp_pointer_warp_v1":                                   {wire_pointerwarp.WpPointerWarpV1Interface, &client_pointerwarp.WpPointerWarp{}, map[uint16]bool{0: true}, nil},
	"wp_presentation":                                      {wire_presentationtime.WpPresentationInterface, &client_presentation.WpPresentation{}, map[uint16]bool{0: true}, nil},
	"wp_presentation_feedback":                             {wire_presentationtime.WpPresentationFeedbackInterface, &client_presentation.WpPresentationFeedback{}, nil, map[uint16]bool{1: true, 2: true}},
	"wp_tearing_control_manager_v1":                        {wire_tearingcontrol.WpTearingControlManagerV1Interface, &client_tearingcontrol.WpTearingControlManager{}, map[uint16]bool{0: true}, nil},
	"wp_tearing_control_v1":                                {wire_tearingcontrol.WpTearingControlV1Interface, &client_tearingcontrol.WpTearingControl{}, map[uint16]bool{1: true}, nil},
	"wp_viewport":                                          {wire_viewporter.WpViewportInterface, &client_viewporter.WpViewport{}, map[uint16]bool{0: true}, nil},
	"wp_viewporter":                                        {wire_viewporter.WpViewporterInterface, &client_viewporter.WpViewporter{}, map[uint16]bool{0: true}, nil},
	"xdg_activation_token_v1":                              {wire_xdgactivation.ActivationTokenV1Interface, &client_xdgactivation.XdgActivationToken{}, map[uint16]bool{4: true}, nil},
	"xdg_activation_v1":                                    {wire_xdgactivation.ActivationV1Interface, &client_xdgactivation.XdgActivation{}, map[uint16]bool{0: true}, nil},
	"xdg_popup":                                            {wire_xdgshell.PopupInterface, &client_xdgshell.XdgPopup{}, map[uint16]bool{0: true}, nil},
	"xdg_positioner":                                       {wire_xdgshell.PositionerInterface, &client_xdgshell.XdgPositioner{}, map[uint16]bool{0: true}, nil},
	"xdg_surface":                                          {wire_xdgshell.SurfaceInterface, &client_xdgshell.XdgSurface{}, map[uint16]bool{0: true}, nil},
	"xdg_toplevel":                                         {wire_xdgshell.ToplevelInterface, &client_xdgshell.XdgToplevel{}, map[uint16]bool{0: true}, nil},
	"xdg_wm_base":                                          {wire_xdgshell.WmBaseInterface, &client_xdgshell.XdgWmBase{}, map[uint16]bool{0: true}, nil},
	"zwlr_foreign_toplevel_handle_v1":                      {wire_wlrforeigntoplevel.ZwlrForeignToplevelHandleV1Interface, &client_foreigntoplevel.ForeignToplevelHandle{}, map[uint16]bool{7: true}, nil},
	"zwlr_foreign_toplevel_manager_v1":                     {wire_wlrforeigntoplevel.ZwlrForeignToplevelManagerV1Interface, &client_foreigntoplevel.ForeignToplevelManager{}, nil, map[uint16]bool{1: true}},
	"zwlr_layer_shell_v1":                                  {wire_wlrlayershell.ZwlrLayerShellV1Interface, &client_layershell.LayerShell{}, map[uint16]bool{1: true}, nil},
	"zwlr_layer_surface_v1":                                {wire_wlrlayershell.ZwlrLayerSurfaceV1Interface, &client_layershell.LayerSurface{}, map[uint16]bool{7: true}, nil},
	"zwlr_output_configuration_head_v1":                    {wire_wlroutputmanagement.ZwlrOutputConfigurationHeadV1Interface, &client_outputmanagement.OutputConfigurationHead{}, nil, nil},
	"zwlr_output_configuration_v1":                         {wire_wlroutputmanagement.ZwlrOutputConfigurationV1Interface, &client_outputmanagement.OutputConfiguration{}, map[uint16]bool{4: true}, nil},
	"zwlr_output_head_v1":                                  {wire_wlroutputmanagement.ZwlrOutputHeadV1Interface, &client_outputmanagement.OutputHead{}, map[uint16]bool{0: true}, nil},
	"zwlr_output_manager_v1":                               {wire_wlroutputmanagement.ZwlrOutputManagerV1Interface, &client_outputmanagement.OutputManager{}, nil, map[uint16]bool{2: true}},
	"zwlr_output_mode_v1":                                  {wire_wlroutputmanagement.ZwlrOutputModeV1Interface, &client_outputmanagement.OutputMode{}, map[uint16]bool{0: true}, nil},
	"zwlr_output_power_manager_v1":                         {wire_wlroutputpower.ZwlrOutputPowerManagerV1Interface, &client_outputpower.OutputPowerManager{}, map[uint16]bool{1: true}, nil},
	"zwlr_output_power_v1":                                 {wire_wlroutputpower.ZwlrOutputPowerV1Interface, &client_outputpower.OutputPower{}, map[uint16]bool{1: true}, nil},
	"zwlr_screencopy_frame_v1":                             {wire_wlrscreencopy.ZwlrScreencopyFrameV1Interface, &client_screencopy.ScreencopyFrame{}, map[uint16]bool{1: true}, nil},
	"zwlr_screencopy_manager_v1":                           {wire_wlrscreencopy.ZwlrScreencopyManagerV1Interface, &client_screencopy.ScreencopyManager{}, map[uint16]bool{2: true}, nil},
	"zwp_confined_pointer_v1":                              {wire_pointerconstraints.ZwpConfinedPointerV1Interface, &client_pointerconstraints.ConfinedPointer{}, map[uint16]bool{0: true}, nil},
	"zwp_idle_inhibit_manager_v1":                          {wire_idleinhibit.ZwpIdleInhibitManagerV1Interface, &client_idleinhibit.IdleInhibitManager{}, map[uint16]bool{0: true}, nil},
	"zwp_idle_inhibitor_v1":                                {wire_idleinhibit.ZwpIdleInhibitorV1Interface, &client_idleinhibit.IdleInhibitor{}, map[uint16]bool{0: true}, nil},
	"zwp_input_method_keyboard_grab_v2":                    {wire_inputmethod.ZwpInputMethodKeyboardGrabV2Interface, &client_inputmethod.InputMethodKeyboardGrab{}, map[uint16]bool{0: true}, nil},
	"zwp_input_method_manager_v2":                          {wire_inputmethod.ZwpInputMethodManagerV2Interface, &client_inputmethod.InputMethodManager{}, map[uint16]bool{1: true}, nil},
	"zwp_input_method_v2":                                  {wire_inputmethod.ZwpInputMethodV2Interface, &client_inputmethod.InputMethod{}, map[uint16]bool{6: true}, nil},
	"zwp_input_popup_surface_v2":                           {wire_inputmethod.ZwpInputPopupSurfaceV2Interface, &client_inputmethod.InputPopupSurface{}, map[uint16]bool{0: true}, nil},
	"zwp_keyboard_shortcuts_inhibit_manager_v1":            {wire_keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1Interface, &client_shortcutsinhibit.KeyboardShortcutsInhibitManager{}, map[uint16]bool{0: true}, nil},
	"zwp_keyboard_shortcuts_inhibitor_v1":                  {wire_keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitorV1Interface, &client_shortcutsinhibit.KeyboardShortcutsInhibitor{}, map[uint16]bool{0: true}, nil},
	"zwp_linux_buffer_params_v1":                           {wire_linuxdmabuf.ZwpLinuxBufferParamsV1Interface, &client_linuxdmabuf.LinuxBufferParams{}, map[uint16]bool{0: true}, nil},
	"zwp_linux_dmabuf_feedback_v1":                         {wire_linuxdmabuf.ZwpLinuxDmabufFeedbackV1Interface, &client_linuxdmabuf.LinuxDmabufFeedback{}, map[uint16]bool{0: true}, nil},
	"zwp_linux_dmabuf_v1":                                  {wire_linuxdmabuf.ZwpLinuxDmabufV1Interface, &client_linuxdmabuf.LinuxDmabuf{}, map[uint16]bool{0: true}, nil},
	"zwp_locked_pointer_v1":                                {wire_pointerconstraints.ZwpLockedPointerV1Interface, &client_pointerconstraints.LockedPointer{}, map[uint16]bool{0: true}, nil},
	"zwp_pointer_constraints_v1":                           {wire_pointerconstraints.ZwpPointerConstraintsV1Interface, &client_pointerconstraints.PointerConstraints{}, map[uint16]bool{0: true}, nil},
	"zwp_primary_selection_device_manager_v1":              {wire_primaryselection.ZwpPrimarySelectionDeviceManagerV1Interface, &client_primaryselection.PrimarySelectionDeviceManager{}, map[uint16]bool{2: true}, nil},
	"zwp_primary_selection_device_v1":                      {wire_primaryselection.ZwpPrimarySelectionDeviceV1Interface, &client_primaryselection.PrimarySelectionDevice{}, map[uint16]bool{1: true}, nil},
	"zwp_primary_selection_offer_v1":                       {wire_primaryselection.ZwpPrimarySelectionOfferV1Interface, &client_primaryselection.PrimarySelectionOffer{}, map[uint16]bool{1: true}, nil},
	"zwp_primary_selection_source_v1":                      {wire_primaryselection.ZwpPrimarySelectionSourceV1Interface, &client_primaryselection.PrimarySelectionSource{}, map[uint16]bool{1: true}, nil},
	"zwp_relative_pointer_manager_v1":                      {wire_relativepointer.ZwpRelativePointerManagerV1Interface, &client_relativepointer.RelativePointerManager{}, map[uint16]bool{0: true}, nil},
	"zwp_relative_pointer_v1":                              {wire_relativepointer.ZwpRelativePointerV1Interface, &client_relativepointer.RelativePointer{}, map[uint16]bool{0: true}, nil},
	"zwp_text_input_manager_v3":                            {wire_textinput.ZwpTextInputManagerV3Interface, &client_textinput.TextInputManagerV3{}, map[uint16]bool{0: true}, nil},
	"zwp_text_input_v3":                                    {wire_textinput.ZwpTextInputV3Interface, &client_textinput.TextInputV3{}, map[uint16]bool{0: true}, nil},
	"zwp_virtual_keyboard_manager_v1":                      {wire_virtualkeyboard.ZwpVirtualKeyboardManagerV1Interface, &client_virtualkeyboard.VirtualKeyboardManager{}, nil, nil},
	"zwp_virtual_keyboard_v1":                              {wire_virtualkeyboard.ZwpVirtualKeyboardV1Interface, &client_virtualkeyboard.VirtualKeyboard{}, map[uint16]bool{3: true}, nil},
	"zxdg_output_manager_v1":                               {wire_xdgoutput.ZxdgOutputManagerV1Interface, &client_xdgoutput.ZxdgOutputManager{}, map[uint16]bool{0: true}, nil},
	"zxdg_output_v1":                                       {wire_xdgoutput.ZxdgOutputV1Interface, &client_xdgoutput.ZxdgOutput{}, map[uint16]bool{0: true}, nil},
}

type wireObjectKey struct {
	c  *wlturbo.Display
	id uint32
}

var wireObjects sync.Map // wireObjectKey -> *wireReceiver; display cleanup removes all entries

// wireReceiver is a real wire event receiver, not a replacement project service.
// It can keep a quiet receiver until a test installs its observer at the same ID.
type wireReceiver struct {
	wlturbo.BaseProxy
	c        *wlturbo.Display
	schema   wireSchema
	observer wlturbo.Proxy
}

func (p *wireReceiver) EventSignature(op uint16) (string, bool) {
	if p.schema.events != nil {
		return p.schema.events.EventSignature(op)
	}
	if p.schema.iface == nil || int(op) >= len(p.schema.iface.Events) {
		return "", false
	}
	return wireSignatureText(p.schema.iface.Events[op].Signature), true
}
func wireSignatureText(sig string) string {
	var b strings.Builder
	for _, ch := range sig {
		switch ch {
		case 'i':
			b.WriteString("int,")
		case 'u':
			b.WriteString("uint,")
		case 'f':
			b.WriteString("fixed,")
		case 's':
			b.WriteString("string,")
		case 'o':
			b.WriteString("object,")
		case 'n':
			b.WriteString("new_id,")
		case 'a':
			b.WriteString("array,")
		case 'h':
			b.WriteString("fd,")
		}
	}
	return b.String()
}
func (p *wireReceiver) Dispatch(e *wlturbo.Event) {
	// Server-created objects must exist before an observer reads their events.
	if p.schema.iface != nil && int(e.Opcode) < len(p.schema.iface.Events) {
		m := p.schema.iface.Events[e.Opcode]
		// A separate cursor preserves the observer's event offset and FD ownership.
		pos := 0
		arg := 0
		for _, ch := range m.Signature {
			if ch == '?' || ch >= '0' && ch <= '9' {
				continue
			}
			if ch == 'n' && arg < len(m.Types) && m.Types[arg] != nil && pos+4 <= len(e.Data()) {
				id := binary.NativeEndian.Uint32(e.Data()[pos:])
				setWireSchema(p.c, id, m.Types[arg].Name, p.Version())
			}
			switch ch {
			case 'h':
			case 's', 'a':
				if pos+4 <= len(e.Data()) {
					n := int(binary.NativeEndian.Uint32(e.Data()[pos:]))
					pos += 4 + (n+3)&^3
				}
			default:
				pos += 4
			}
			arg++
		}
	}
	if p.observer != nil {
		p.observer.Dispatch(e)
	}
	if p.schema.eventDestructors[e.Opcode] {
		p.c.Context().Unregister(p)
		wireObjects.Delete(wireObjectKey{p.c, p.ID()})
	}
}
func wireObject(c *wlturbo.Display, id uint32) *wireReceiver {
	k := wireObjectKey{c, id}
	if v, ok := wireObjects.Load(k); ok {
		return v.(*wireReceiver)
	}
	p := &wireReceiver{c: c}
	p.SetID(id)
	p.SetContext(c.Context())
	actual, _ := wireObjects.LoadOrStore(k, p)
	return actual.(*wireReceiver)
}
func registerWireProxy(c *wlturbo.Display, observer wlturbo.Proxy) {
	p := wireObject(c, observer.ID())
	if setter, ok := observer.(interface{ SetContext(*wlturbo.Context) }); ok {
		setter.SetContext(c.Context())
	}
	p.observer = observer
	c.Context().Register(p)
}
func setWireSchema(c *wlturbo.Display, id uint32, iface string, version uint32) {
	schema, ok := wireSchemas[iface]
	if !ok {
		panic("missing raw-wire test schema: " + iface)
	}
	p := wireObject(c, id)
	p.schema = schema
	p.SetVersion(version)
	if setter, ok := p.observer.(interface{ SetVersion(uint32) }); ok {
		setter.SetVersion(version)
	}
	c.Context().Register(p)
}
func bindWireID(c *wlturbo.Display, name uint32, iface string, version uint32) (uint32, error) {
	id, err := c.Registry().BindID(name, iface, version)
	if err == nil {
		setWireSchema(c, id, iface, version)
	}
	return id, err
}
func wireRequest(c *wlturbo.Display, id uint32, op uint16, fds []int, args ...any) error {
	v, ok := wireObjects.Load(wireObjectKey{c, id})
	if !ok {
		return c.SendRequestWithFDs(id, op, fds, args...)
	} // intentional nonexistent target
	p := v.(*wireReceiver)
	if p.schema.iface != nil && int(op) < len(p.schema.iface.Requests) {
		m := p.schema.iface.Requests[op]
		arg, typeIndex := 0, 0
		for _, ch := range m.Signature {
			if ch == '?' || ch >= '0' && ch <= '9' {
				continue
			}
			// Raw Display.SendRequestWithFDs omits FD placeholders from args.
			if ch == 'h' {
				typeIndex++
				continue
			}
			if ch == 'n' && typeIndex < len(m.Types) && m.Types[typeIndex] != nil && arg < len(args) {
				if child, ok := args[arg].(uint32); ok && child != 0 {
					setWireSchema(c, child, m.Types[typeIndex].Name, p.Version())
				}
			}
			arg++
			typeIndex++
		}
		if p.schema.requestDestructors[op] {
			err := c.Context().SendDestructorWithFDs(p, uint32(op), fds, args...)
			if err == nil {
				wireObjects.Delete(wireObjectKey{c, id})
			}
			return err
		}
	}
	return c.SendRequestWithFDs(id, op, fds, args...)
}
func forgetWireClient(c *wlturbo.Display) {
	wireObjects.Range(func(k, v any) bool {
		if k.(wireObjectKey).c == c {
			wireObjects.Delete(k)
		}
		return true
	})
}

// retireWireObject handles protocol-defined destruction caused by another
// object (for example configuration heads). No request is sent on the child.
func retireWireObject(c *wlturbo.Display, id uint32) {
	if v, ok := wireObjects.LoadAndDelete(wireObjectKey{c, id}); ok {
		c.Context().Unregister(v.(*wireReceiver))
	}
}

// Catch drift between the generated server descriptions used for new_id
// bookkeeping and the canonical generated client schemas used by dispatch.
func TestRawWireSchemasMatchGeneratedProtocols(t *testing.T) {
	for name, schema := range wireSchemas {
		t.Run(name, func(t *testing.T) {
			if schema.events != nil {
				for op, event := range schema.iface.Events {
					got, ok := schema.events.EventSignature(uint16(op))
					if !ok || got != wireSignatureText(event.Signature) {
						t.Fatalf("event %s schema=%q supported=%t want=%q", event.Name, got, ok, wireSignatureText(event.Signature))
					}
				}
			}
			for op := range schema.requestDestructors {
				if int(op) >= len(schema.iface.Requests) {
					t.Fatalf("destructor %d outside %s", op, name)
				}
			}
			for op := range schema.eventDestructors {
				if int(op) >= len(schema.iface.Events) {
					t.Fatalf("event destructor %d outside %s", op, name)
				}
			}
		})
	}
}
