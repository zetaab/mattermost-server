// Copyright (c) 2017-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package api4

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/mattermost/mattermost-server/app"
	"github.com/mattermost/mattermost-server/mlog"
	"github.com/mattermost/mattermost-server/model"
)

func (api *API) InitPost() {
	api.BaseRoutes.Posts.Handle("", api.ApiSessionRequired(createPost)).Methods("POST")
	api.BaseRoutes.Post.Handle("", api.ApiSessionRequired(getPost)).Methods("GET")
	api.BaseRoutes.Post.Handle("", api.ApiSessionRequired(deletePost)).Methods("DELETE")
	api.BaseRoutes.Posts.Handle("/ephemeral", api.ApiSessionRequired(createEphemeralPost)).Methods("POST")
	api.BaseRoutes.Post.Handle("/thread", api.ApiSessionRequired(getPostThread)).Methods("GET")
	api.BaseRoutes.Post.Handle("/files/info", api.ApiSessionRequired(getFileInfosForPost)).Methods("GET")
	api.BaseRoutes.PostsForChannel.Handle("", api.ApiSessionRequired(getPostsForChannel)).Methods("GET")
	api.BaseRoutes.PostsForUser.Handle("/flagged", api.ApiSessionRequired(getFlaggedPostsForUser)).Methods("GET")

	api.BaseRoutes.ChannelForUser.Handle("/posts/unread", api.ApiSessionRequired(getPostsForChannelAroundLastUnread)).Methods("GET")

	api.BaseRoutes.Team.Handle("/posts/search", api.ApiSessionRequired(searchPosts)).Methods("POST")
	api.BaseRoutes.Post.Handle("", api.ApiSessionRequired(updatePost)).Methods("PUT")
	api.BaseRoutes.Post.Handle("/patch", api.ApiSessionRequired(patchPost)).Methods("PUT")
	api.BaseRoutes.Post.Handle("/actions/{action_id:[A-Za-z0-9]+}", api.ApiSessionRequired(doPostAction)).Methods("POST")
	api.BaseRoutes.Post.Handle("/pin", api.ApiSessionRequired(pinPost)).Methods("POST")
	api.BaseRoutes.Post.Handle("/unpin", api.ApiSessionRequired(unpinPost)).Methods("POST")
}

func createPost(c *Context, w http.ResponseWriter, r *http.Request) {
	post := model.PostFromJson(r.Body)
	if post == nil {
		c.SetInvalidParam("post")
		return
	}

	post.UserId = c.Session.UserId

	hasPermission := false
	if c.App.SessionHasPermissionToChannel(c.Session, post.ChannelId, model.PERMISSION_CREATE_POST) {
		hasPermission = true
	} else if channel, err := c.App.GetChannel(post.ChannelId); err == nil {
		// Temporary permission check method until advanced permissions, please do not copy
		if channel.Type == model.CHANNEL_OPEN && c.App.SessionHasPermissionToTeam(c.Session, channel.TeamId, model.PERMISSION_CREATE_POST_PUBLIC) {
			hasPermission = true
		}
	}

	if !hasPermission {
		c.SetPermissionError(model.PERMISSION_CREATE_POST)
		return
	}

	if post.CreateAt != 0 && !c.App.SessionHasPermissionTo(c.Session, model.PERMISSION_MANAGE_SYSTEM) {
		post.CreateAt = 0
	}

	rp, err := c.App.CreatePostAsUser(c.App.PostWithProxyRemovedFromImageURLs(post))
	if err != nil {
		c.Err = err
		return
	}

	c.App.SetStatusOnline(c.Session.UserId, false)
	c.App.UpdateLastActivityAtIfNeeded(c.Session)

	clientPost, err := c.App.PreparePostForClient(rp)
	if err != nil {
		mlog.Error("Failed to prepare post for createPost response", mlog.Any("err", err))
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(clientPost.ToJson()))
}

func createEphemeralPost(c *Context, w http.ResponseWriter, r *http.Request) {
	ephRequest := model.PostEphemeral{}

	json.NewDecoder(r.Body).Decode(&ephRequest)
	if ephRequest.UserID == "" {
		c.SetInvalidParam("user_id")
		return
	}

	if ephRequest.Post == nil {
		c.SetInvalidParam("post")
		return
	}

	ephRequest.Post.UserId = c.Session.UserId
	ephRequest.Post.CreateAt = model.GetMillis()

	if !c.App.SessionHasPermissionTo(c.Session, model.PERMISSION_CREATE_POST_EPHEMERAL) {
		c.SetPermissionError(model.PERMISSION_CREATE_POST_EPHEMERAL)
		return
	}

	rp := c.App.SendEphemeralPost(ephRequest.UserID, c.App.PostWithProxyRemovedFromImageURLs(ephRequest.Post))

	clientPost, err := c.App.PreparePostForClient(rp)
	if err != nil {
		mlog.Error("Failed to prepare post for createEphemeralPost response", mlog.Any("err", err))
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(clientPost.ToJson()))
}

func getPostsForChannel(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequireChannelId()
	if c.Err != nil {
		return
	}

	afterPost := r.URL.Query().Get("after")
	beforePost := r.URL.Query().Get("before")
	sinceString := r.URL.Query().Get("since")

	var since int64
	var parseError error

	var nextPostId string
	var previousPostId string

	if len(sinceString) > 0 {
		since, parseError = strconv.ParseInt(sinceString, 10, 64)
		if parseError != nil {
			c.SetInvalidParam("since")
			return
		}
	}

	if !c.App.SessionHasPermissionToChannel(c.Session, c.Params.ChannelId, model.PERMISSION_READ_CHANNEL) {
		c.SetPermissionError(model.PERMISSION_READ_CHANNEL)
		return
	}

	var list *model.PostList
	var err *model.AppError
	etag := ""

	if since > 0 {
		list, err = c.App.GetPostsSince(c.Params.ChannelId, since, app.MAX_LIMIT_POSTS_SINCE)
	} else if len(afterPost) > 0 {
		etag = c.App.GetPostsEtag(c.Params.ChannelId)

		if c.HandleEtag(etag, "Get Posts After", w, r) {
			return
		}

		list, err = c.App.GetPostsAfterPost(c.Params.ChannelId, afterPost, c.Params.Page, c.Params.PerPage)
		if len(list.Order) > 0 {
			previousPostId = afterPost
		}
	} else if len(beforePost) > 0 {
		etag = c.App.GetPostsEtag(c.Params.ChannelId)

		if c.HandleEtag(etag, "Get Posts Before", w, r) {
			return
		}

		list, err = c.App.GetPostsBeforePost(c.Params.ChannelId, beforePost, c.Params.Page, c.Params.PerPage)
		if len(list.Order) > 0 {
			nextPostId = beforePost
		}
	} else {
		etag = c.App.GetPostsEtag(c.Params.ChannelId)

		if c.HandleEtag(etag, "Get Posts", w, r) {
			return
		}

		list, err = c.App.GetPostsPage(c.Params.ChannelId, c.Params.Page, c.Params.PerPage)
	}

	if err != nil {
		c.Err = err
		return
	}

	if len(etag) > 0 {
		w.Header().Set(model.HEADER_ETAG_SERVER, etag)
	}

	clientPostList, err := c.App.PreparePostListForClient(list)
	if err != nil {
		mlog.Error("Failed to prepare posts for getPostsForChannel response", mlog.Any("err", err))
	}

	if nextPostId == "" {
		nextPostId = c.App.GetNextPostFromPostList(clientPostList)
	}

	if previousPostId == "" {
		previousPostId = c.App.GetPreviousPostFromPostList(clientPostList)
	}

	clientPostList.NextPostId = nextPostId
	clientPostList.PreviousPostId = previousPostId

	w.Write([]byte(clientPostList.ToJson()))
}

func getPostsForChannelAroundLastUnread(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequireUserId().RequireChannelId()
	if c.Err != nil {
		return
	}

	userId := c.Params.UserId
	if !c.App.SessionHasPermissionToUser(c.Session, userId) {
		c.SetPermissionError(model.PERMISSION_EDIT_OTHER_USERS)
		return
	}

	channelId := c.Params.ChannelId
	if !c.App.SessionHasPermissionToChannel(c.Session, channelId, model.PERMISSION_READ_CHANNEL) {
		c.SetPermissionError(model.PERMISSION_READ_CHANNEL)
		return
	}

	postList, err := c.App.GetPostsForChannelAroundLastUnread(channelId, userId, c.Params.LimitBefore, c.Params.LimitAfter)
	if err != nil {
		c.Err = err
		return
	}

	etag := ""
	if len(postList.Order) == 0 {
		etag = c.App.GetPostsEtag(channelId)

		if c.HandleEtag(etag, "Get Posts", w, r) {
			return
		}

		postList, err = c.App.GetPostsPage(channelId, app.PAGE_DEFAULT, c.Params.LimitBefore)
	}

	clientPostList, err := c.App.PreparePostListForClient(postList)
	if err != nil {
		mlog.Error("Failed to prepare posts for getPostsForChannelAroundLastUnread response", mlog.Any("err", err))
	}

	clientPostList.NextPostId = c.App.GetNextPostFromPostList(clientPostList)
	clientPostList.PreviousPostId = c.App.GetPreviousPostFromPostList(clientPostList)

	if len(etag) > 0 {
		w.Header().Set(model.HEADER_ETAG_SERVER, etag)
	}
	w.Write([]byte(clientPostList.ToJson()))
}

func getFlaggedPostsForUser(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequireUserId()
	if c.Err != nil {
		return
	}

	if !c.App.SessionHasPermissionToUser(c.Session, c.Params.UserId) {
		c.SetPermissionError(model.PERMISSION_EDIT_OTHER_USERS)
		return
	}

	channelId := r.URL.Query().Get("channel_id")
	teamId := r.URL.Query().Get("team_id")

	var posts *model.PostList
	var err *model.AppError

	if len(channelId) > 0 {
		posts, err = c.App.GetFlaggedPostsForChannel(c.Params.UserId, channelId, c.Params.Page, c.Params.PerPage)
	} else if len(teamId) > 0 {
		posts, err = c.App.GetFlaggedPostsForTeam(c.Params.UserId, teamId, c.Params.Page, c.Params.PerPage)
	} else {
		posts, err = c.App.GetFlaggedPosts(c.Params.UserId, c.Params.Page, c.Params.PerPage)
	}

	if err != nil {
		c.Err = err
		return
	}

	clientPostList, err := c.App.PreparePostListForClient(posts)
	if err != nil {
		mlog.Error("Failed to prepare posts for getFlaggedPostsForUser response", mlog.Any("err", err))
	}

	w.Write([]byte(clientPostList.ToJson()))
}

func getPost(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequirePostId()
	if c.Err != nil {
		return
	}

	var post *model.Post
	var err *model.AppError
	if post, err = c.App.GetSinglePost(c.Params.PostId); err != nil {
		c.Err = err
		return
	}

	var channel *model.Channel
	if channel, err = c.App.GetChannel(post.ChannelId); err != nil {
		c.Err = err
		return
	}

	if !c.App.SessionHasPermissionToChannel(c.Session, channel.Id, model.PERMISSION_READ_CHANNEL) {
		if channel.Type == model.CHANNEL_OPEN {
			if !c.App.SessionHasPermissionToTeam(c.Session, channel.TeamId, model.PERMISSION_READ_PUBLIC_CHANNEL) {
				c.SetPermissionError(model.PERMISSION_READ_PUBLIC_CHANNEL)
				return
			}
		} else {
			c.SetPermissionError(model.PERMISSION_READ_CHANNEL)
			return
		}
	}

	post, err = c.App.PreparePostForClient(post)
	if err != nil {
		mlog.Error("Failed to prepare post for getPost response", mlog.Any("err", err))
	}

	if c.HandleEtag(post.Etag(), "Get Post", w, r) {
		return
	}

	w.Header().Set(model.HEADER_ETAG_SERVER, post.Etag())
	w.Write([]byte(post.ToJson()))
}

func deletePost(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequirePostId()
	if c.Err != nil {
		return
	}

	post, err := c.App.GetSinglePost(c.Params.PostId)
	if err != nil {
		c.SetPermissionError(model.PERMISSION_DELETE_POST)
		return
	}

	if c.Session.UserId == post.UserId {
		if !c.App.SessionHasPermissionToChannel(c.Session, post.ChannelId, model.PERMISSION_DELETE_POST) {
			c.SetPermissionError(model.PERMISSION_DELETE_POST)
			return
		}
	} else {
		if !c.App.SessionHasPermissionToChannel(c.Session, post.ChannelId, model.PERMISSION_DELETE_OTHERS_POSTS) {
			c.SetPermissionError(model.PERMISSION_DELETE_OTHERS_POSTS)
			return
		}
	}

	if _, err := c.App.DeletePost(c.Params.PostId, c.Session.UserId); err != nil {
		c.Err = err
		return
	}

	ReturnStatusOK(w)
}

func getPostThread(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequirePostId()
	if c.Err != nil {
		return
	}

	var list *model.PostList
	var err *model.AppError
	if list, err = c.App.GetPostThread(c.Params.PostId); err != nil {
		c.Err = err
		return
	}

	var post *model.Post
	if val, ok := list.Posts[c.Params.PostId]; ok {
		post = val
	} else {
		c.SetInvalidUrlParam("post_id")
		return
	}

	var channel *model.Channel
	if channel, err = c.App.GetChannel(post.ChannelId); err != nil {
		c.Err = err
		return
	}

	if !c.App.SessionHasPermissionToChannel(c.Session, channel.Id, model.PERMISSION_READ_CHANNEL) {
		if channel.Type == model.CHANNEL_OPEN {
			if !c.App.SessionHasPermissionToTeam(c.Session, channel.TeamId, model.PERMISSION_READ_PUBLIC_CHANNEL) {
				c.SetPermissionError(model.PERMISSION_READ_PUBLIC_CHANNEL)
				return
			}
		} else {
			c.SetPermissionError(model.PERMISSION_READ_CHANNEL)
			return
		}
	}

	if c.HandleEtag(list.Etag(), "Get Post Thread", w, r) {
		return
	}

	clientPostList, err := c.App.PreparePostListForClient(list)
	if err != nil {
		mlog.Error("Failed to prepare posts for getFlaggedPostsForUser response", mlog.Any("err", err))
	}

	w.Header().Set(model.HEADER_ETAG_SERVER, clientPostList.Etag())

	w.Write([]byte(clientPostList.ToJson()))
}

func searchPosts(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequireTeamId()
	if c.Err != nil {
		return
	}

	if !c.App.SessionHasPermissionToTeam(c.Session, c.Params.TeamId, model.PERMISSION_VIEW_TEAM) {
		c.SetPermissionError(model.PERMISSION_VIEW_TEAM)
		return
	}

	includeDeletedChannels := r.URL.Query().Get("include_deleted_channels") == "true"

	props := model.StringInterfaceFromJson(r.Body)

	terms, ok := props["terms"].(string)
	if !ok || len(terms) == 0 {
		c.SetInvalidParam("terms")
		return
	}

	timeZoneOffset, ok := props["time_zone_offset"].(float64)
	if !ok {
		timeZoneOffset = 0
	}

	isOrSearch, _ := props["is_or_search"].(bool)

	startTime := time.Now()

	results, err := c.App.SearchPostsInTeam(terms, c.Session.UserId, c.Params.TeamId, isOrSearch, includeDeletedChannels, int(timeZoneOffset))

	elapsedTime := float64(time.Since(startTime)) / float64(time.Second)
	metrics := c.App.Metrics
	if metrics != nil {
		metrics.IncrementPostsSearchCounter()
		metrics.ObservePostsSearchDuration(elapsedTime)
	}

	if err != nil {
		c.Err = err
		return
	}

	clientPostList, err := c.App.PreparePostListForClient(results.PostList)
	if err != nil {
		mlog.Error("Failed to prepare posts for searchPosts response", mlog.Any("err", err))
	}

	results = model.MakePostSearchResults(clientPostList, results.Matches)

	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write([]byte(results.ToJson()))
}

func updatePost(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequirePostId()
	if c.Err != nil {
		return
	}

	post := model.PostFromJson(r.Body)

	if post == nil {
		c.SetInvalidParam("post")
		return
	}

	if !c.App.SessionHasPermissionToChannelByPost(c.Session, c.Params.PostId, model.PERMISSION_EDIT_POST) {
		c.SetPermissionError(model.PERMISSION_EDIT_POST)
		return
	}

	originalPost, err := c.App.GetSinglePost(c.Params.PostId)
	if err != nil {
		c.SetPermissionError(model.PERMISSION_EDIT_POST)
		return
	}

	if c.Session.UserId != originalPost.UserId {
		if !c.App.SessionHasPermissionToChannelByPost(c.Session, c.Params.PostId, model.PERMISSION_EDIT_OTHERS_POSTS) {
			c.SetPermissionError(model.PERMISSION_EDIT_OTHERS_POSTS)
			return
		}
	}

	post.Id = c.Params.PostId

	rpost, err := c.App.UpdatePost(c.App.PostWithProxyRemovedFromImageURLs(post), false)
	if err != nil {
		c.Err = err
		return
	}

	rpost, err = c.App.PreparePostForClient(rpost)
	if err != nil {
		mlog.Error("Failed to prepare post for updatePost response", mlog.Any("err", err))
	}

	w.Write([]byte(rpost.ToJson()))
}

func patchPost(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequirePostId()
	if c.Err != nil {
		return
	}

	post := model.PostPatchFromJson(r.Body)

	if post == nil {
		c.SetInvalidParam("post")
		return
	}

	if !c.App.SessionHasPermissionToChannelByPost(c.Session, c.Params.PostId, model.PERMISSION_EDIT_POST) {
		c.SetPermissionError(model.PERMISSION_EDIT_POST)
		return
	}

	originalPost, err := c.App.GetSinglePost(c.Params.PostId)
	if err != nil {
		c.SetPermissionError(model.PERMISSION_EDIT_POST)
		return
	}

	if c.Session.UserId != originalPost.UserId {
		if !c.App.SessionHasPermissionToChannelByPost(c.Session, c.Params.PostId, model.PERMISSION_EDIT_OTHERS_POSTS) {
			c.SetPermissionError(model.PERMISSION_EDIT_OTHERS_POSTS)
			return
		}
	}

	patchedPost, err := c.App.PatchPost(c.Params.PostId, c.App.PostPatchWithProxyRemovedFromImageURLs(post))
	if err != nil {
		c.Err = err
		return
	}

	patchedPost, err = c.App.PreparePostForClient(patchedPost)
	if err != nil {
		mlog.Error("Failed to prepare post for patchPost response", mlog.Any("err", err))
	}

	w.Write([]byte(patchedPost.ToJson()))
}

func saveIsPinnedPost(c *Context, w http.ResponseWriter, r *http.Request, isPinned bool) {
	c.RequirePostId()
	if c.Err != nil {
		return
	}

	if !c.App.SessionHasPermissionToChannelByPost(c.Session, c.Params.PostId, model.PERMISSION_READ_CHANNEL) {
		c.SetPermissionError(model.PERMISSION_READ_CHANNEL)
		return
	}

	patch := &model.PostPatch{}
	patch.IsPinned = model.NewBool(isPinned)

	_, err := c.App.PatchPost(c.Params.PostId, patch)
	if err != nil {
		c.Err = err
		return
	}

	ReturnStatusOK(w)
}

func pinPost(c *Context, w http.ResponseWriter, r *http.Request) {
	saveIsPinnedPost(c, w, r, true)
}

func unpinPost(c *Context, w http.ResponseWriter, r *http.Request) {
	saveIsPinnedPost(c, w, r, false)
}

func getFileInfosForPost(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequirePostId()
	if c.Err != nil {
		return
	}

	if !c.App.SessionHasPermissionToChannelByPost(c.Session, c.Params.PostId, model.PERMISSION_READ_CHANNEL) {
		c.SetPermissionError(model.PERMISSION_READ_CHANNEL)
		return
	}

	infos, err := c.App.GetFileInfosForPost(c.Params.PostId, false)
	if err != nil {
		c.Err = err
		return
	}

	if c.HandleEtag(model.GetEtagForFileInfos(infos), "Get File Infos For Post", w, r) {
		return
	}

	w.Header().Set("Cache-Control", "max-age=2592000, public")
	w.Header().Set(model.HEADER_ETAG_SERVER, model.GetEtagForFileInfos(infos))
	w.Write([]byte(model.FileInfosToJson(infos)))
}

func doPostAction(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequirePostId().RequireActionId()
	if c.Err != nil {
		return
	}

	if !c.App.SessionHasPermissionToChannelByPost(c.Session, c.Params.PostId, model.PERMISSION_READ_CHANNEL) {
		c.SetPermissionError(model.PERMISSION_READ_CHANNEL)
		return
	}

	actionRequest := model.DoPostActionRequestFromJson(r.Body)
	if actionRequest == nil {
		actionRequest = &model.DoPostActionRequest{}
	}

	if err := c.App.DoPostAction(c.Params.PostId, c.Params.ActionId, c.Session.UserId, actionRequest.SelectedOption); err != nil {
		c.Err = err
		return
	}

	ReturnStatusOK(w)
}
